"""scripts/apptown-restore-check.py: a change's read-time words (what it waits
on, what its landing approval would ask now) and the model's narrative may
differ after an upgrade without counting as lost records; a recorded field
that differs is still named, with both sides of the first difference."""
import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("restore_check", os.path.join(HERE, "apptown-restore-check.py"))
check = importlib.util.module_from_spec(spec)
spec.loader.exec_module(check)


def page(**over):
    body = {
        "change": {"id": "c1", "title": "Add a page", "requestMet": "no", "requestGap": "the heading was asked for"},
        "detail": {"landingMeans": ["it merges and ships on my say-so."], "state": "waiting"},
        "waitingOn": {"kind": "approval", "says": "waiting for you or another approver"},
        "evidence": {"approvals": [{"by": "sam", "means": "it merges and ships on my say-so."}]},
    }
    body.update(over)
    return body


def recorded(body):
    real = check.must
    check.must = lambda base, path: body
    try:
        return check.recorded_change("http://source", "c1")
    finally:
        check.must = real


class RecordedChange(unittest.TestCase):
    def test_read_time_words_and_narrative_are_not_compared(self):
        before = recorded(page())
        after = recorded(page(
            change={"id": "c1", "title": "Add a page", "requestMet": "unknown"},
            detail={"landingMeans": ["it merges on my say-so."], "state": "waiting"},
            waitingOn={"kind": "approval", "says": "waiting for approval"},
        ))
        self.assertTrue(all(check.kept(before[k], after.get(k)) for k in before), (before, after))

    def test_a_recorded_approval_sentence_is_still_compared(self):
        before = recorded(page())
        after = recorded(page(evidence={"approvals": [{"by": "sam", "means": "it merges on my say-so."}]}))
        self.assertFalse(check.kept(before["evidence"], after["evidence"]))
        self.assertEqual(
            check.first_difference(before["evidence"], after["evidence"], "evidence"),
            'evidence.approvals[0].means: "it merges and ships on my say-so." -> "it merges on my say-so."')


if __name__ == "__main__":
    unittest.main()
