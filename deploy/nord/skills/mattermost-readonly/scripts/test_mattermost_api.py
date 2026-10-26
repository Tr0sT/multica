import io
import json
import os
import unittest
from unittest.mock import patch
from urllib.error import HTTPError

import mattermost_api as api


class MattermostReadTests(unittest.TestCase):
    def setUp(self):
        env = patch.dict(os.environ, {"MATTERMOST_URL": "https://chat.example/mattermost",
                                      "MATTERMOST_TOKEN": "test-secret"})
        env.start()
        self.addCleanup(env.stop)

    def response(self, body):
        opener = patch.object(api, "build_opener")
        mocked = opener.start()
        self.addCleanup(opener.stop)
        mocked.return_value.open.return_value = io.BytesIO(body)
        return mocked.return_value.open

    def test_read_preserves_server_subpath_and_pagination(self):
        call = self.response(b'{"order": [], "posts": {}}')
        result = api.read("/channels/" + "a" * 26 + "/posts", {"page": 3, "per_page": 200})
        request = call.call_args.args[0]
        self.assertEqual(request.get_method(), "GET")
        self.assertEqual(request.full_url, "https://chat.example/mattermost/api/v4/channels/" + "a" * 26 + "/posts?page=3&per_page=200")
        self.assertEqual(request.get_header("Authorization"), "Bearer test-secret")
        self.assertEqual(result, {"order": [], "posts": {}})

    def test_credentials_cannot_be_redirected_by_path_or_base(self):
        for path in ["https://other.example/users/me", "//other.example", "/users/me/tokens", "/users/logout", "/posts", "/../users/me", "/users/%6de", "/users/me?x=1"]:
            with self.subTest(path=path), self.assertRaises(api.MattermostError):
                api.read(path)
        for base in ["http://chat.example", "https://user:pass@chat.example", "https://chat.example?x=1", "https://chat.example/#fragment", "https://chat.example/a/../b", "https://chat.example/%2e%2e"]:
            with self.subTest(base=base), patch.dict(os.environ, {"MATTERMOST_URL": base}), self.assertRaises(api.MattermostError):
                api.read("/users/me")

    def test_redirect_is_refused(self):
        with self.assertRaises(api.MattermostError):
            api.NoRedirects().redirect_request(None, None, 302, "", {}, "https://other.example")

    def test_http_error_never_includes_response_body(self):
        with patch.object(api, "build_opener") as opener:
            opener.return_value.open.side_effect = HTTPError("https://chat.example", 403, "test-secret", {}, io.BytesIO(b"test-secret"))
            with self.assertRaises(api.MattermostError) as caught:
                api.read("/users/me")
        self.assertIn("403", str(caught.exception))
        self.assertNotIn("test-secret", str(caught.exception))

    def test_partial_or_malformed_responses_are_not_reported_as_complete(self):
        for body in [b"x" * (api.MAX_RESPONSE_BYTES + 1), b"not json", b'{"first_inaccessible_post_time": 123}']:
            with self.subTest(size=len(body)):
                self.response(body)
                with self.assertRaises(api.MattermostError):
                    api.read("/posts/" + "b" * 26 + "/thread", {"perPage": 200})

    def test_cli_redacts_a_token_echoed_by_server(self):
        self.response(json.dumps({"message": "test-secret"}).encode())
        with patch("sys.argv", ["mattermost_api.py", "/users/me"]), patch("sys.stdout", new_callable=io.StringIO) as output:
            self.assertEqual(api.main(), 0)
        self.assertNotIn("test-secret", output.getvalue())
        self.assertIn("[redacted]", output.getvalue())


if __name__ == "__main__":
    unittest.main()
