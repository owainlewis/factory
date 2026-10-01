"""Run trusted tests in a fresh process against a candidate app.py."""

import argparse
import importlib.util
import json
import sys
import unittest
from pathlib import Path


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("workspace", type=Path)
    parser.add_argument("tests", type=Path, nargs="+")
    args = parser.parse_args()
    sys.path.insert(0, str(args.workspace.resolve()))
    suite = unittest.TestSuite()
    for index, path in enumerate(args.tests):
        spec = importlib.util.spec_from_file_location(f"eval_test_{index}", path)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        suite.addTests(unittest.defaultTestLoader.loadTestsFromModule(module))
    result = unittest.TextTestRunner(stream=sys.stderr, verbosity=2).run(suite)
    passed = result.wasSuccessful() and result.testsRun > 0 and not result.skipped
    print(
        json.dumps(
            {
                "passed": passed,
                "tests": result.testsRun,
                "failures": len(result.failures),
                "errors": len(result.errors),
                "skipped": len(result.skipped),
            }
        )
    )
    return 0 if passed else 1


if __name__ == "__main__":
    sys.exit(main())
