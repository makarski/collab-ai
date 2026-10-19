"""Host scripts verify HTTPS on any Python, and name the fix when they cannot."""

from pathlib import Path
import ssl
import sys
import unittest
from unittest.mock import patch
from urllib.error import URLError

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import sandbox_https


class FakeContext:
    def __init__(self, authorities, cafile=None):
        self.authorities, self.cafile = authorities, cafile

    def cert_store_stats(self):
        return {"x509": self.authorities, "crl": 0, "x509_ca": self.authorities}


def factory(authorities):
    """ssl.create_default_context stand-in: the platform store has `authorities` CAs."""
    def create(cafile=None):
        return FakeContext(authorities if cafile is None else 1, cafile)
    return create


class ContextTests(unittest.TestCase):
    def test_platform_store_is_used_when_it_trusts_anything(self):
        context = sandbox_https.context(create=factory(128), bundle=lambda: "/unused.pem")
        self.assertIsNone(context.cafile)

    def test_certifi_fills_an_empty_store(self):
        context = sandbox_https.context(create=factory(0), bundle=lambda: "/certifi/cacert.pem")
        self.assertEqual(context.cafile, "/certifi/cacert.pem")

    def test_nothing_to_trust_names_the_fix(self):
        with self.assertRaises(ValueError) as caught:
            sandbox_https.context(create=factory(0), bundle=lambda: None)
        self.assertIn("Install Certificates.command", str(caught.exception))

    def test_missing_certifi_is_no_bundle(self):
        with patch.dict(sys.modules, {"certifi": None}):
            self.assertIsNone(sandbox_https.certifi_bundle())


class UrlopenTests(unittest.TestCase):
    def test_passes_a_verifying_context(self):
        context = FakeContext(1)
        with patch.object(sandbox_https, "context", return_value=context), \
                patch.object(sandbox_https.urllib.request, "urlopen", return_value="response") as opened:
            self.assertEqual(sandbox_https.urlopen("https://example.test", timeout=7), "response")
        opened.assert_called_once_with("https://example.test", timeout=7, context=context)

    def test_certificate_failure_reports_cause_and_fix(self):
        failure = ssl.SSLCertVerificationError()
        failure.verify_message = "unable to get local issuer certificate"
        with patch.object(sandbox_https, "context", return_value=FakeContext(1)), \
                patch.object(sandbox_https.urllib.request, "urlopen", side_effect=URLError(failure)):
            with self.assertRaises(ValueError) as caught:
                sandbox_https.urlopen("https://example.test/asset", timeout=7)
        message = str(caught.exception)
        self.assertIn("https://example.test/asset", message)
        self.assertIn("unable to get local issuer certificate", message)
        self.assertIn("Install Certificates.command", message)

    def test_other_network_errors_pass_through(self):
        with patch.object(sandbox_https, "context", return_value=FakeContext(1)), \
                patch.object(sandbox_https.urllib.request, "urlopen", side_effect=URLError("offline")):
            with self.assertRaises(URLError):
                sandbox_https.urlopen("https://example.test", timeout=7)


if __name__ == "__main__":
    unittest.main()
