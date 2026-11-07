"""Django's test runner with one JUnit report and readable console output."""
import os

from app.settings import *  # noqa: F401,F403

TEST_RUNNER = "xmlrunner.extra.djangotestrunner.XMLTestRunner"
# AppTown supplies the exact file path, rather than a directory of reports.
# Keep this in test settings so the deployed app needs no report environment.
TEST_OUTPUT_DIR = os.path.dirname(os.environ["APPTOWN_TEST_REPORT"]) or "."
TEST_OUTPUT_FILE_NAME = os.path.basename(os.environ["APPTOWN_TEST_REPORT"])
TEST_OUTPUT_VERBOSE = 2
