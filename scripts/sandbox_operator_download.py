"""Retry transient failures when fetching the pinned OpenTofu archive."""

import sys
import time
from urllib.error import HTTPError
from urllib.request import urlopen


def download_archive(url):
    for attempt in range(3):
        try:
            with urlopen(url, timeout=120) as response:
                return response.read()
        except HTTPError as error:
            error.close()
            if error.code not in (408, 429, 500, 502, 503, 504) or attempt == 2:
                raise
            delay = 2 ** attempt
            print(f"OpenTofu download: HTTP {error.code}; retrying in {delay}s "
                  f"(attempt {attempt + 2}/3)", file=sys.stderr, flush=True)
            time.sleep(delay)
