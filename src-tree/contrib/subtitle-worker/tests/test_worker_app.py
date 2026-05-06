import pathlib
import sys
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[1]
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

from app import app


class WorkerAppRoutesTest(unittest.TestCase):
    def test_style_lab_routes_are_removed(self):
        routes = {route.path for route in app.routes}

        self.assertNotIn("/api/v1/style-lab/preview", routes)
        self.assertNotIn("/api/v1/style-lab/sample", routes)


if __name__ == "__main__":
    unittest.main()
