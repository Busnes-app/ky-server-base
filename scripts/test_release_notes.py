"""Release notes must describe the new release, including upgrade impact."""
import runpy
from pathlib import Path
import unittest

check_notes = runpy.run_path(str(Path(__file__).with_name("check-release-notes.py")))["check_notes"]
NOTES = """## Current source
### Changes
- Refuse undersized signing keys before authentication.
### Upgrade impact
- Rotate short signing secrets before restarting.
"""


class ReleaseNotesTests(unittest.TestCase):
    def test_accepts_first_notes_and_changed_release(self):
        check_notes(NOTES, "")
        check_notes(NOTES.replace("undersized", "expired"), NOTES)

    def test_rejects_stale_notes_even_when_archive_changes(self):
        with self.assertRaisesRegex(ValueError, "unchanged"):
            check_notes(NOTES + "\n## Historical images\n- New archive row\n", NOTES)

    def test_rejects_missing_summary_or_upgrade_guidance(self):
        for text in ("", "## Current source\n### Changes\n### Upgrade impact\n",
                     NOTES.replace("- Rotate short signing secrets before restarting.", ""),
                     NOTES.replace("- Refuse undersized signing keys before authentication.", "- <!-- pending -->"),
                     NOTES.replace("- Refuse undersized signing keys before authentication.", "- [Compare](https://example.invalid/compare/a...b)")):
            with self.subTest(text=text), self.assertRaises(ValueError):
                check_notes(text, "")


if __name__ == "__main__":
    unittest.main()
