"""
Integration tests for the example scenario's json-server API.

The API base URL is read from the API_BASE_URL environment variable, defaulting
to the in-cluster DNS name of the test-api service.
"""

import os
import requests
import pytest

BASE_URL = os.environ.get(
    "API_BASE_URL", "http://test-api.test-app.svc.cluster.local"
).rstrip("/")


@pytest.fixture(scope="session")
def posts():
    """Fetch /posts once for the whole session."""
    resp = requests.get(f"{BASE_URL}/posts", timeout=10)
    resp.raise_for_status()
    return resp.json()


def test_get_posts_returns_200():
    resp = requests.get(f"{BASE_URL}/posts", timeout=10)
    assert resp.status_code == 200, f"Expected 200, got {resp.status_code}"


def test_posts_list_has_two_items(posts):
    assert isinstance(posts, list), f"Expected list, got {type(posts)}"
    assert len(posts) == 2, f"Expected 2 posts, got {len(posts)}"


def test_post_has_expected_fields(posts):
    for post in posts:
        for field in ("id", "title", "body"):
            assert field in post, f"Post missing field {field!r}: {post}"


def test_get_post_by_id():
    resp = requests.get(f"{BASE_URL}/posts/1", timeout=10)
    assert resp.status_code == 200, f"Expected 200, got {resp.status_code}"
    data = resp.json()
    assert data.get("id") == 1, f"Expected id=1, got {data.get('id')}"
    assert data.get("title") == "hello", (
        f"Expected title='hello', got {data.get('title')}"
    )
    assert data.get("body") == "world", (
        f"Expected body='world', got {data.get('body')}"
    )
