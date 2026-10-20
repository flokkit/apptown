"""The health probe asks by the app's address; nothing else may.

Seeded with app/health.py. A health probe by address sends its Host as
an IP and port, never a name from APPTOWN_ORIGINS, and no forwarded HTTPS.
/healthz must answer it with 200; every other path must still refuse that
host, so the health answer never widens ALLOWED_HOSTS.
"""
from django.test import SimpleTestCase, override_settings

BY_ADDRESS = "10.0.0.5:8080"


@override_settings(ALLOWED_HOSTS=[".main.example.test"], SECURE_SSL_REDIRECT=True)
class HealthByAddress(SimpleTestCase):
    def test_health_answers_a_probe_by_address(self):
        response = self.client.get("/healthz", HTTP_HOST=BY_ADDRESS)
        self.assertEqual(response.status_code, 200)

    def test_other_paths_still_refuse_a_host_by_address(self):
        for path in ("/", "/healthz/", "/admin/"):
            response = self.client.get(path, HTTP_HOST=BY_ADDRESS)
            self.assertEqual(response.status_code, 400, path)
