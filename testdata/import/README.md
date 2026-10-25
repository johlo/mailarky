# Mailbox import fixtures

`python.mbox` and the Maildir in `python-maildir.tar` were written by Python's
standard `mailbox` module, a writer independent of Mailarky's importer.
`TestImportPythonWrittenFixtures` imports them and checks folders, flags,
headers, line endings, UID order and attachments. Both contain only invented
test messages.

The Maildir is committed as a tar because its file names contain `:`, which
Windows does not allow, and because the archive keeps the file times that set
the UID order.

To regenerate the files, run this from the repository root; the output is
deterministic:

```sh
python3 scripts/generate_import_fixtures.py
```

Update the test's expectations if you change the messages.
