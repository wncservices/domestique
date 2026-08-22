package basemap

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// JobConfig is the standing, deployment-wide configuration an update Job
// runs with — the operator-supplied half, as opposed to a request's own
// bbox/maxzoom/buildDate. Mirrors domestique-chart's basemapUpdate values
// block exactly; main.go builds this from the same config file.
type JobConfig struct {
	// TilesNamespace is where the Job runs and where the tiles PVC lives.
	TilesNamespace string
	// TilesPVCName is the tiles Deployment's own basemap PVC — mounted
	// directly by this Job instead of going through kubectl cp/exec into
	// the running tiles pod. That used to be how the extracted file
	// reached the tiles pod at all, but kubectl cp/exec streaming a
	// multi-gigabyte file through the API server's exec subprotocol
	// proved unreliable in practice (tar stream corruption — "invalid
	// tar magic" — and, in earlier manual attempts at the same transfer,
	// connection resets, both at arbitrary byte offsets). The PVC moved
	// to nfs-client (ReadWriteMany) specifically so this Job and the
	// tiles pod can mount the exact same volume at once — the transfer
	// becomes a local `mv`, no different from the atomic rename this Job
	// already did on the far side of the old kubectl cp, just without
	// anything streaming through the API server in between.
	TilesPVCName          string
	ExtractImage          string // repository:tag
	CopyImage             string // repository:tag
	CPURequest            string
	MemRequest            string
	MemLimit              string
	ActiveDeadlineSeconds int64
}

// Client wraps a Kubernetes clientset with the config an update Job needs,
// so callers never touch client-go types directly.
type Client struct {
	clientset kubernetes.Interface
	cfg       JobConfig
}

// InCluster builds a Client from the pod's own mounted ServiceAccount —
// nil, nil when not running in a cluster (a laptop, most tests), the same
// "quietly unavailable" shape Komoot/Garmin/Wahoo already use for a
// deployment-wide credential that simply isn't there.
func InCluster(cfg JobConfig) (*Client, error) {
	restCfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, nil //nolint:nilerr // deliberate: see doc comment
	}
	clientset, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("build kubernetes client: %w", err)
	}
	return &Client{clientset: clientset, cfg: cfg}, nil
}

const basemapVolume = "basemap"
const basemapMountPath = "/basemap"

// stagingPath is where extract writes — never the live-serving filename,
// so a mid-extract Job never leaves nginx serving a half-written file; the
// copy container's mv onto finalPath (same filesystem, so an atomic
// rename) is what actually publishes it.
const stagingPath = basemapMountPath + "/basemap.pmtiles.new"
const finalPath = basemapMountPath + "/basemap.pmtiles"

// runAsNonRootUser is an arbitrary non-zero UID, not the images' own —
// confirmed via `docker run --user 1000:1000` that both protomaps/go-pmtiles
// and alpine/k8s start and run correctly under it. Explicit, rather than
// runAsNonRoot: true alone: that combination is exactly what made the
// pre-promotion AnalysisTemplate's curl container fail outright
// ("cannot verify user is non-root") against an image whose own USER is a
// non-numeric name kubelet cannot statically verify — an explicit numeric
// UID here sidesteps that regardless of what either image declares.
const runAsNonRootUser = 1000

// jobTTLSeconds is how long a finished Job (and its Pod) sticks around
// before the Kubernetes TTL controller deletes it — 24 hours, generous
// enough that an admin checking back the next day still finds the copy
// container's own log (Outcome reads it to report the final size or a
// failure message), while still eventually cleaning up after itself. This
// app's own RBAC (domestique-chart's basemap-rbac.yaml) deliberately does
// not grant delete on jobs — the TTL controller runs with its own
// privileges, not this app's, so nothing here needs to.
const jobTTLSeconds = 24 * 60 * 60

// Trigger creates the update Job and returns its generated name.
func (c *Client) Trigger(ctx context.Context, bbox BBox, maxZoom int, buildDate string) (string, error) {
	falseVal := false
	trueVal := true
	uid := int64(runAsNonRootUser)

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "domestique-basemap-update-",
			Namespace:    c.cfg.TilesNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "domestique",
				"domestique.dev/purpose":       "basemap-update",
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            ptr(int32(0)),
			ActiveDeadlineSeconds:   ptr(c.cfg.ActiveDeadlineSeconds),
			TTLSecondsAfterFinished: ptr(int32(jobTTLSeconds)),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					// Neither container calls the Kubernetes API anymore
					// — extract and copy both just read/write the
					// mounted PVC directly — so there is no
					// ServiceAccount to name here at all, and nothing to
					// mount a token for.
					AutomountServiceAccountToken: &falseVal,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: &trueVal,
						RunAsUser:    &uid,
						RunAsGroup:   &uid,
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
					InitContainers: []corev1.Container{
						{
							Name:  "extract",
							Image: c.cfg.ExtractImage,
							// Direct argv, not a shell — bbox/buildDate are
							// formatted from validated floats/ints, never
							// string-concatenated into something a shell
							// would reinterpret, so there is nothing here
							// for an admin-supplied bbox to inject into.
							Args: []string{
								"extract",
								"https://build.protomaps.com/" + buildDate + ".pmtiles",
								stagingPath,
								// %.6f, not %g: %g switches to exponential
								// notation for values near zero (0.00001 ->
								// "1e-05"), a valid bbox coordinate this
								// project's own Validate allows through. Fixed
								// notation is always an ordinary decimal,
								// which is both what a human reading this
								// Job's spec expects and one less format
								// go-pmtiles' own flag parser has to get
								// right.
								fmt.Sprintf("--bbox=%.6f,%.6f,%.6f,%.6f", bbox.West, bbox.South, bbox.East, bbox.North),
								fmt.Sprintf("--maxzoom=%d", maxZoom),
							},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: &falseVal,
								ReadOnlyRootFilesystem:   &trueVal,
								Capabilities: &corev1.Capabilities{
									Drop: []corev1.Capability{"ALL"},
								},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse(c.cfg.CPURequest),
									corev1.ResourceMemory: resource.MustParse(c.cfg.MemRequest),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceMemory: resource.MustParse(c.cfg.MemLimit),
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{Name: basemapVolume, MountPath: basemapMountPath},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name:    "copy",
							Image:   c.cfg.CopyImage,
							Command: []string{"sh", "-c"},
							// mv on the same volume is an atomic rename —
							// nginx (a separate pod mounting this same
							// PVC) never sees a half-written file — the
							// exact guarantee the old kubectl cp/exec
							// version of this had too, just without a
							// multi-gigabyte transfer riding through the
							// API server's exec subprotocol to get there.
							Args: []string{
								`set -eu
mv "$STAGING_FILE" "$FINAL_FILE"
size=$(wc -c < "$FINAL_FILE" | tr -d ' ')
echo "placed basemap"
echo "SIZE_BYTES=$size"
`,
							},
							Env: []corev1.EnvVar{
								{Name: "STAGING_FILE", Value: stagingPath},
								{Name: "FINAL_FILE", Value: finalPath},
							},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: &falseVal,
								ReadOnlyRootFilesystem:   &trueVal,
								Capabilities: &corev1.Capabilities{
									Drop: []corev1.Capability{"ALL"},
								},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("50m"),
									corev1.ResourceMemory: resource.MustParse("64Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceMemory: resource.MustParse("128Mi"),
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								// Not read-only, unlike the old version of
								// this mount: this container is the one
								// that now actually writes the final file
								// (the mv above), where before it only
								// ever read from an emptyDir to stream
								// elsewhere via kubectl cp.
								{Name: basemapVolume, MountPath: basemapMountPath},
								{Name: "tmp", MountPath: "/tmp"},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							// The tiles Deployment's own PVC, mounted
							// directly — nfs-client (ReadWriteMany)
							// specifically so this Job and the tiles pod
							// can both hold it at once. See TilesPVCName's
							// own doc comment for why this replaced an
							// emptyDir + kubectl cp/exec.
							Name: basemapVolume,
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: c.cfg.TilesPVCName,
								},
							},
						},
						{
							Name:         "tmp",
							VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
						},
					},
				},
			},
		},
	}

	created, err := c.clientset.BatchV1().Jobs(c.cfg.TilesNamespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("create basemap update job: %w", err)
	}
	return created.Name, nil
}

// JobOutcome is what became of a triggered Job, for the status endpoint to
// report without a caller needing to know anything about Kubernetes.
type JobOutcome struct {
	// Done is false while the Job is still running.
	Done bool
	// Succeeded is only meaningful when Done is true.
	Succeeded bool
	// Message explains a failure. Empty on success.
	Message string
	// SizeBytes is the placed archive's size, parsed from the copy
	// container's own SIZE_BYTES= log line. Only meaningful on success;
	// zero (not an error) if the line could not be read or parsed —
	// logs are best-effort, and a missing size is not worth failing an
	// otherwise-successful update over.
	SizeBytes int64
}

// Outcome reports what happened to a previously triggered Job.
func (c *Client) Outcome(ctx context.Context, jobName string) (JobOutcome, error) {
	job, err := c.clientset.BatchV1().Jobs(c.cfg.TilesNamespace).Get(ctx, jobName, metav1.GetOptions{})
	if err != nil {
		return JobOutcome{}, fmt.Errorf("get basemap update job: %w", err)
	}

	switch {
	case job.Status.Succeeded > 0:
		log := c.copyContainerLog(ctx, jobName)
		return JobOutcome{Done: true, Succeeded: true, SizeBytes: parseSizeBytes(log)}, nil
	case job.Status.Failed > 0:
		log := c.copyContainerLog(ctx, jobName)
		msg := lastNonEmptyLine(log)
		if msg == "" {
			msg = "the job failed — see its pod logs for details"
		}
		return JobOutcome{Done: true, Succeeded: false, Message: msg}, nil
	default:
		return JobOutcome{Done: false}, nil
	}
}

// progressPattern matches go-pmtiles' own progress line, e.g.:
//
//	fetching chunks  62% |████████████        | (5.4/8.7 GB, 24 MB/s) [3m22s:1m7s]
//
// It is a single, continuously-overwritten line (carriage returns, not
// newlines, between updates — confirmed against the real binary), so
// finding every match in a raw log dump and taking the last one is what
// gets the most recent update, not a line-based parse.
var progressPattern = regexp.MustCompile(`fetching chunks\s+(\d{1,3})%`)

// Progress reports how far along the download half of a running Job is, as
// a percentage — best-effort, like Outcome's own log reads: nil, false
// while nothing parseable has been printed yet (the Job just started,
// still resolving which tiles to fetch) rather than an error, since a
// status endpoint should stay usable even when this can't be determined.
func (c *Client) Progress(ctx context.Context, jobName string) (percent int, ok bool) {
	pods, err := c.clientset.CoreV1().Pods(c.cfg.TilesNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: "job-name=" + jobName,
	})
	if err != nil || len(pods.Items) == 0 {
		return 0, false
	}
	req := c.clientset.CoreV1().Pods(c.cfg.TilesNamespace).GetLogs(pods.Items[0].Name, &corev1.PodLogOptions{
		Container: "extract",
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		return 0, false
	}
	defer func() { _ = stream.Close() }()
	// 512KB is generous headroom: the crew's own first real extract (a
	// ~6-minute, 8.7GB Western Europe download) produced well under 100KB
	// of progress-line updates in total.
	data, _ := io.ReadAll(io.LimitReader(stream, 512<<10))
	return parseProgress(data)
}

// parseProgress finds the last progressPattern match in a raw log dump and
// returns it as a percentage — split out from Progress so this parsing
// logic is testable without a Kubernetes client.
func parseProgress(data []byte) (percent int, ok bool) {
	matches := progressPattern.FindAllSubmatch(data, -1)
	if len(matches) == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(string(matches[len(matches)-1][1]))
	if err != nil || n < 0 || n > 100 {
		return 0, false
	}
	return n, true
}

// copyContainerLog reads the copy container's own log for whichever pod the
// Job ran, best-effort — a status report is still useful without it, so a
// log-read failure here is swallowed rather than surfaced as this call's
// own error.
func (c *Client) copyContainerLog(ctx context.Context, jobName string) string {
	pods, err := c.clientset.CoreV1().Pods(c.cfg.TilesNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: "job-name=" + jobName,
	})
	if err != nil || len(pods.Items) == 0 {
		return ""
	}
	req := c.clientset.CoreV1().Pods(c.cfg.TilesNamespace).GetLogs(pods.Items[0].Name, &corev1.PodLogOptions{
		Container: "copy",
		TailLines: ptr(int64(10)),
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		return ""
	}
	defer func() { _ = stream.Close() }()
	// io.ReadAll, not a single Read call: io.Reader is free to return less
	// than the full buffer even when more data is immediately available, so
	// one Read here could silently truncate the log before the
	// SIZE_BYTES= line the copy script always prints last — parseSizeBytes
	// would then find nothing and a genuinely successful update would get
	// recorded with sizeBytes: 0, with no error anywhere to explain why.
	data, _ := io.ReadAll(io.LimitReader(stream, 4096))
	return string(data)
}

// parseSizeBytes finds the copy script's own `SIZE_BYTES=<n>` line.
func parseSizeBytes(log string) int64 {
	for _, line := range strings.Split(log, "\n") {
		if n, ok := strings.CutPrefix(line, "SIZE_BYTES="); ok {
			if v, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64); err == nil {
				return v
			}
		}
	}
	return 0
}

func lastNonEmptyLine(log string) string {
	lines := strings.Split(strings.TrimRight(log, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return strings.TrimSpace(lines[i])
		}
	}
	return ""
}

func ptr[T any](v T) *T { return &v }
