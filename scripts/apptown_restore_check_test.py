"""scripts/apptown-restore-check.py: a change's read-time words (what it waits
on, what its landing approval would ask now) and the model's narrative may
differ after an upgrade without counting as lost records, on the page and in
its run detail; every recorded field that differs is still named, with both
sides."""
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
        "detail": {"landingMeans": ["it merges and ships on my say-so."], "state": "waiting",
                   "waitingOn": {"says": "waiting for you or another approver"}, "next": [{"act": "approve"}]},
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
            detail={"landingMeans": ["it merges on my say-so."], "state": "waiting",
                    "waitingOn": {"says": "waiting for approval"}, "next": []},
            waitingOn={"kind": "approval", "says": "waiting for approval"},
        ))
        self.assertTrue(all(check.kept(before[k], after.get(k)) for k in before), (before, after))

    def test_a_running_stage_elapsed_time_is_read_time_in_both_release_lists(self):
        def releases(elapsed, number=48):
            r = {"number": number, "stage": "awaiting-approval", "stageElapsedMs": elapsed}
            return {"approvals": [], "releases": [dict(r)], "landingReleases": [dict(r)]}
        before = recorded(page(evidence=releases(15399442)))
        after = recorded(page(evidence=releases(16373431)))
        self.assertTrue(check.kept(before["evidence"], after["evidence"]), (before, after))
        lost = recorded(page(evidence=releases(16373431, number=49)))
        self.assertFalse(check.kept(before["evidence"], lost["evidence"]))

    def test_status_labels_and_the_checks_sentence_are_read_time(self):
        def shown(label, sentence, step_label, step_status="done", passed=6):
            checks = {"text": sentence, "passed": passed}
            return page(statusLabel=label, checks=dict(checks),
                        detail={"checks": dict(checks), "steps": [{"name": "approve", "status": step_status, "statusLabel": step_label}]})
        before = recorded(shown("landed", "Checks not all known", "waiting for checks"))
        after = recorded(shown(None, "Checks not all known \u00b7 6 passed", "waiting for approval"))
        self.assertTrue(all(check.kept(before[k], after.get(k)) for k in before), (before, after))
        lost = recorded(shown(None, "Checks not all known", "waiting for checks", step_status="failed", passed=5))
        self.assertEqual(check.differences(before["checks"], lost["checks"], "checks"), ["checks.passed: 6 -> 5"])
        self.assertEqual(check.differences(before["detail"], lost["detail"], "detail"), [
            "detail.checks.passed: 6 -> 5", 'detail.steps[0].status: "done" -> "failed"'])

    def test_a_recorded_approval_sentence_is_still_compared(self):
        before = recorded(page())
        after = recorded(page(evidence={"approvals": [{"by": "rae", "means": "it merges on my say-so."}]}))
        self.assertFalse(check.kept(before["evidence"], after["evidence"]))
        self.assertEqual(check.differences(before["evidence"], after["evidence"], "evidence"), [
            'evidence.approvals[0].by: "sam" -> "rae"',
            'evidence.approvals[0].means: "it merges and ships on my say-so." -> "it merges on my say-so."'])


if __name__ == "__main__":
    unittest.main()
