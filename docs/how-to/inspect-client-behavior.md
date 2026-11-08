# Assert client behaviour with command history

Use an account's command history to verify retry counts, backoff timing, and
connection reuse after a [rate limit](test-rate-limits.md) or
[injected failure](fault-injection.md). Configure the application to authenticate
both SMTP and IMAP with the test account's credentials.

## Capture one scenario

Clear history after client setup and before starting the operation under test:

```sh
curl -fsS -X DELETE http://localhost:8026/api/v1/accounts/ACCOUNT_ID/history
```

Run the application operation and wait for its completion or expected failure.
Read commands for that account:

```sh
curl -fsS 'http://localhost:8026/api/v1/accounts/ACCOUNT_ID/history?protocol=imap&command=NOOP&limit=1000'
```

Change `command` to the operation being tested. For example, use `DATA` for an
SMTP submission, `RCPT` for recipient failures, or URL-encoded `UID%20FETCH` for
mail synchronization. Clear history only when earlier operations have finished;
commands already in progress at the clear are discarded when they finish.

## Assert retry count and delay

The following assertion expects one failed IMAP NOOP, followed by one successful
retry at least one second after the rejection. Run it after your application
scenario, using that scenario's account ID:

```sh
MAILARKY_ACCOUNT_API=http://localhost:8026/api/v1/accounts/ACCOUNT_ID python3 - <<'PY'
from datetime import datetime
import json
import os
import urllib.request

base = os.environ["MAILARKY_ACCOUNT_API"].rstrip("/")
with urllib.request.urlopen(base + "/history?protocol=imap&command=NOOP&limit=1000", timeout=10) as response:
    history = json.load(response)
assert history["dropped"] == 0, "History overflowed; the scenario is incomplete"
assert not history["has_more"], "Read the remaining pages before asserting counts"
attempts = history["commands"]
assert len(attempts) == 2, attempts
assert [entry["response"] for entry in attempts] == ["NO [LIMIT]", "OK"]

def timestamp(value):
    return datetime.fromisoformat(value.replace("Z", "+00:00"))

wait = timestamp(attempts[1]["started_at"]) - timestamp(attempts[0]["completed_at"])
assert wait.total_seconds() >= 1, f"Retried too soon: {wait}"
assert attempts[0]["connection_id"] == attempts[1]["connection_id"], "Unexpected reconnect"
print("One retry, sufficient backoff, and connection reuse verified")
PY
```

For SMTP, check `451 4.7.0` followed by a successful `250` response. Use the
backoff interval required by your client. Server completion precedes the
client's receipt of the reply, so the measured gap includes transport time.
For parallel operations, group entries by `connection_id` and the operation's
context before interpreting retries; identical commands can belong to different
workflows.

For a permanent recipient rejection, filter SMTP `RCPT` entries by their
`recipient` field and assert that the rejected address was not retried during
the scenario. For an injected disconnect, assert `outcome == "disconnect"`
and check that subsequent attempts have a different connection ID. Use HTTP
message inspection separately to check delivery effects: a lost acknowledgement
can leave mail stored despite the disconnect.

## Poll a running test

Read `/history?after=0&limit=100`, then pass the returned `next_after` as `after`
on the next request. Keep the same filters while following a cursor. Continue
through `has_more` pages and poll for later completions. IDs follow completion
order; timestamps show when each command started. Active IDLE and incomplete
DATA commands appear only when they finish or disconnect.

History retains 1,000 entries per account and is not persisted. Check `dropped`
on every page when completeness matters. Credentials, MIME contents, raw reply
text, and arbitrary command arguments are omitted. See the
[HTTP history reference](../reference/http-api.md#command-history) for all fields
and coverage limits.
