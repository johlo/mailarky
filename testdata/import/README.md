# Mailbox import fixtures

`python.mbox` and `python-maildir/` were written by Python's standard `mailbox`
module, a writer independent of Mailarky's importer. `TestImportPythonWrittenFixtures`
imports them and checks folders, flags, headers and attachments.

They contain only invented test messages. To regenerate them, run this from
the repository root; the output is deterministic:

```sh
python3 scripts/generate_import_fixtures.py
```

Update the test's expectations if you change the messages.
