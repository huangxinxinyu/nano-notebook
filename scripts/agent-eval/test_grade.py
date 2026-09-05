import unittest

import grade


class GradeTest(unittest.TestCase):
    def test_judge_cannot_omit_or_invent_rubric_items(self):
        rubric = ["mechanism", "limitation"]
        good = {"checks": [True, False], "evidence_supported": True,
                "no_fabrication": True, "basis": ["mechanism described", "limit absent"]}
        self.assertEqual(grade.validate(good, rubric), good)
        for bad in [{**good, "checks": [True]}, {**good, "checks": [True, "false"]},
                    {**good, "evidence_supported": None}, {**good, "basis": []}]:
            with self.assertRaises(ValueError):
                grade.validate(bad, rubric)

    def test_report_links_are_deduplicated_without_link_labels(self):
        self.assertEqual(grade.links("[one](https://arxiv.org/abs/2210.03629) "
                                     "[two](https://arxiv.org/abs/2210.03629) "
                                     "[source](https://example.com/a#section)"),
                         ["https://arxiv.org/abs/2210.03629", "https://example.com/a"])


if __name__ == "__main__":
    unittest.main()
