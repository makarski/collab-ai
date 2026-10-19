"""HTTPS for host scripts on any Python.

A python.org build on macOS trusts no certificate authority until its
"Install Certificates.command" has been run, so every download fails with a
bare CERTIFICATE_VERIFY_FAILED. Host scripts open HTTPS through here: the
platform trust store when it has any authority, otherwise certifi's bundle,
otherwise an error that names the fix.
"""

import ssl
import urllib.request
from urllib.error import URLError

NO_TRUST = "this Python trusts no certificate authority, so HTTPS downloads cannot be verified"
FIX = ("on macOS with a python.org build, run \"/Applications/Python 3.X/Install Certificates.command\"; "
       "otherwise use a Python with system certificates (macOS: /usr/bin/python3) or install certifi")


def certifi_bundle():
    try:
        import certifi
    except ImportError:
        return None
    return certifi.where()


def context(create=ssl.create_default_context, bundle=certifi_bundle):
    """A verifying TLS context, or ValueError when nothing can be trusted."""
    default = create()
    if default.cert_store_stats()["x509_ca"]:
        return default
    cafile = bundle()
    if cafile is None:
        raise ValueError(f"{NO_TRUST}; {FIX}")
    return create(cafile=cafile)


def urlopen(url, timeout, **kwargs):
    """urllib.request.urlopen with a verifying context; a certificate failure
    reports its cause and the fix instead of the bare SSL error."""
    try:
        return urllib.request.urlopen(url, timeout=timeout, context=context(), **kwargs)
    except URLError as error:
        if isinstance(error.reason, ssl.SSLCertVerificationError):
            raise ValueError(f"certificate verification failed for {url}: {error.reason.verify_message}. "
                             f"If this Python trusts no certificate authority, {FIX}") from error
        raise
