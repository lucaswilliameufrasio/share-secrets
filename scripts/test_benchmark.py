"""Pure report-accounting tests; real HTTP integration lives in cmd/benchmark."""
import unittest

from benchmark import resource_summary, retained_window_valid


class ResourceSummaryTest(unittest.TestCase):
    def test_failed_or_empty_retained_seed_stops_followups(self):
        self.assertFalse(retained_window_valid({"failures": {"terminal seed failed": 1}, "offered": 0}))
        self.assertFalse(retained_window_valid({"failures": {}, "offered": 0}))
        self.assertTrue(retained_window_valid({"failures": {}, "offered": 250}))

    def test_excludes_setup_and_teardown_samples(self):
        result = {"windowStartUnixNs": 100, "windowEndUnixNs": 200, "resources": []}
        for timestamp, seconds, cpu, rss in ((50, 0, 0, 9999), (110, 1, 1, 1024**2),
                                            (190, 2, 1.5, 2 * 1024**2), (250, 3, 5, 9999)):
            point = {"cpuSeconds": cpu, "rssBytes": rss}
            result["resources"].append({"unixNs": timestamp, "seconds": seconds,
                                        "server": point, "generator": point})
        summary = resource_summary(result)
        self.assertEqual(summary["serverCPUcores"], .5)
        self.assertEqual(summary["serverPeakRSSMiB"], 2)

    def test_absent_samples_are_not_synthesized(self):
        summary = resource_summary({"resources": []})
        self.assertNotIn("oomKills", summary)
        self.assertEqual(summary["serverPeakRSSMiB"], 0)


if __name__ == "__main__":
    unittest.main()
