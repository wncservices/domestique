package llm

import (
	"sort"
	"testing"
)

// A strict server needs every required key to be a declared property (and,
// with additionalProperties false, a property that is not required can never be
// sent), so the two lists must agree in every object schema of every job.
func TestEveryJobSchemaRequiresExactlyItsProperties(t *testing.T) {
	schemas := map[string]map[string]any{
		"goal":           goalSchema,
		"profile":        profileSchema,
		"checkin":        checkInSchema,
		"explain":        explainSchema,
		"explainWorkout": explainWorkoutSchema,
		"changes":        proseSchema(100),
		"weekly":         proseSchema(100),
		"planEditsNone":  planEditSchema(Capabilities{}),
		"planEditsAll":   planEditSchema(Capabilities{SwapAlternate: true, ConvertIndoor: true}),
	}
	for name, s := range schemas {
		checkSchemaKeys(t, name, s)
	}
}

func checkSchemaKeys(t *testing.T, path string, node any) {
	t.Helper()
	switch n := node.(type) {
	case map[string]any:
		if props, ok := n["properties"].(map[string]any); ok {
			var req []string
			switch r := n["required"].(type) {
			case []string:
				req = r
			case []any:
				for _, v := range r {
					req = append(req, v.(string))
				}
			}
			inReq := map[string]bool{}
			for _, k := range req {
				inReq[k] = true
				if _, ok := props[k]; !ok {
					t.Errorf("%s: %q is required but not in properties", path, k)
				}
			}
			var keys []string
			for k := range props {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if !inReq[k] {
					t.Errorf("%s: %q is a property but not required", path, k)
				}
				checkSchemaKeys(t, path+"."+k, props[k])
			}
		}
		if items, ok := n["items"]; ok {
			checkSchemaKeys(t, path+"[]", items)
		}
	}
}
