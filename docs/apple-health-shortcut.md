# Apple Health shortcut

This shortcut sends your Apple Watch recovery data (HRV, resting heart rate and sleep) to
Domestique, so readiness can use it. Build it once on an iPhone, or install the shared copy from
the link in Settings. Then add an automation so it runs by itself.

You need the token from Settings, shown once when you connect. If you lose it, press
**Regenerate** in Settings. The old token stops working at once.

## Build the shortcut

Open the Shortcuts app and make a new shortcut. Add these actions in this order.

1. **Ask for Input**. Type: Text. Prompt: "Token". Name the variable `Token`.

2. **Find Health Samples** (HRV):
   - Type: Heart Rate Variability;
   - Start Date: is in the last 14 days;
   - Sort by Start Date, ascending;
   - Limit: off.

3. **Find Health Samples** (resting heart rate):
   - Type: Resting Heart Rate;
   - the same date filter, sort and limit as step 2.

4. **Find Health Samples** (sleep):
   - Type: Sleep Analysis;
   - the same date filter, sort and limit as step 2.

5. **Repeat with Each** over the HRV samples. Inside it, add a **Dictionary** with two items:
   - `start` = Start Date of the repeat item, formatted as ISO 8601;
   - `value` = Value of the repeat item.

   Add a **Add to Variable** action after the Dictionary, to a list named `HRV`.

6. **Repeat with Each** over the resting heart rate samples. Same as step 5, with a list named
   `RestingHR`.

7. **Repeat with Each** over the sleep samples. Inside it, add a **Dictionary** with three items:
   - `start` = Start Date, formatted as ISO 8601;
   - `end` = End Date, formatted as ISO 8601;
   - `value` = Value.

   Add to a list named `Sleep`.

   The Value of a sleep sample is the stage name, such as `Core` or `Awake`. Leave it as it is.
   The server counts `Asleep`, `Core`, `Deep`, `REM` and `Unspecified` as sleep. It ignores
   `In Bed` and `Awake`.

8. **Dictionary** for the body, with three items:
   - `hrv` = the `HRV` list;
   - `restingHr` = the `RestingHR` list;
   - `sleep` = the `Sleep` list.

9. **Get Contents of URL**:
   - URL: `https://<host>/api/wellness/apple`, where `<host>` is your Domestique address;
   - Method: POST;
   - Headers: `Authorization` = `Bearer ` (with the space after Bearer), followed by the `Token`
     variable;
   - Request Body: JSON, with the dictionary from step 8.

   The request must have `Content-Type: application/json`. Shortcuts sets it for a JSON body.

Shortcuts sends times with their offset, as the server needs. Do not change the date format to one
without an offset.

## Automation

1. Open the **Automation** tab and tap **+**.
2. Choose **Sleep**, then **When I Wake Up**. If your iOS version lacks it, use **Alarm is
   Stopped** instead.
3. Choose **Run Immediately**. Turn off **Notify When Run**.
4. Choose your shortcut.

The phone must be unlocked for the first morning's run, because Health data is locked while the
phone is locked.

## Share the shortcut

This step is for whoever maintains the shared copy. Riders can skip it.

1. Open the shortcut's menu and choose **Share**.
2. Choose **Copy iCloud Link**.
3. Set `apple_health.shortcut_url` in the deployment config to the link. Only links that start
   with `https://www.icloud.com/shortcuts/` are accepted.

Riders then get the link in Settings, and the shared copy asks for the token like the one you
built.

## If a run finds nothing

A run on a locked phone sends nothing, or sends days with no readings. The server writes the
days it receives. The next run sends the last 14 days again, so a missed morning is filled in
then. A day sent again overwrites the older values for that day.
