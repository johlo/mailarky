# Isolate parallel tests with accounts

Create one account in test setup and delete it in teardown. Use its username
and password for **both SMTP and IMAP**.

```sh
curl -fsS http://localhost:8026/api/v1/accounts \
  -H 'Content-Type: application/json' -d '{"name":"test-order-42"}'
```

The response contains `id`, `username`, `password`, `recipients`, and `api_base`.
Save the password when creating the account; subsequent GET requests omit it.

1. Configure the application's SMTP and IMAP clients with those credentials.
2. Seed incoming messages at `POST {api_base}/messages`.
3. Let the application send to any customer addresses it normally uses.
4. Assert on `{api_base}/messages?query=folder:Sent` and add account faults at
   `{api_base}/faults` when needed.
5. Delete `/api/v1/accounts/{id}` in a teardown/finally block.

Two tests may send to the same recipient and reuse the same Message-ID because
AUTH selects their separate stores. Rules, message IDs, UID spaces, and event
streams are account-local. Do not use the shared `default` account for parallel
tests that modify the same fixtures or rules.

If the application cannot authenticate SMTP, send to a returned `recipients`
address or register exact recipients during creation. Registered addresses
must be unique because anonymous delivery has no other account identity.
A transaction targeting multiple registered accounts delivers a copy to each.

Deleting an account closes its store and prevents further authentication.
Previously registered addresses are rejected until reassigned, rather than
falling back into the default account. See [design](../explanation/design.md)
for delivery and transaction semantics.
