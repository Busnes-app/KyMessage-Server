"""Prove the strong-key overlay changes nothing but the app's GODEBUG; starts nothing."""

import json
import os
from pathlib import Path
import subprocess
import tempfile

root = Path(__file__).resolve().parents[1]
chains = [
    [],
    ["docker-compose.build.yml"],
    ["docker-compose.lan-dns.yml"],
    ["docker-compose.build.yml", "docker-compose.lan-dns.yml"],
    ["docker-compose.proxy.yml"],
    ["docker-compose.proxy.yml", "docker-compose.static-ip.yml"],
    ["docker-compose.proxy.yml", "docker-compose.matrix.yml"],
    ["docker-compose.proxy.yml", "docker-compose.matrix.yml", "docker-compose.static-ip.yml"],
    ["docker-compose.build.yml", "docker-compose.proxy.yml", "docker-compose.matrix.yml", "docker-compose.turn.yml",
     "docker-compose.notes.yml", "docker-compose.notes-build.yml"],
]
with tempfile.TemporaryDirectory(prefix="kymessages-profile-check-") as directory:
    fixture = Path(directory) / ".env"
    fixture.write_text(
        "KY_ADMIN_PASSWORD=check-only-password\n"
        "KY_UID=1111\nKY_GID=2222\nKY_MATRIX_UID=1234\nKY_MATRIX_GID=5678\n"
        "KY_DNS=192.0.2.53\n"
        "KY_APP_URL=https://chat.example.invalid\n"
        "KY_SESSION_SECRET=" + "ab" * 32 + "\n"
        "KY_TRUSTED_PROXIES=10.91.0.10\nKY_CONTAINER_IP=10.91.0.20\nKY_NETWORK_SUBNET=10.91.0.0/24\n"
        "KY_KYIDENTITY_HMAC_SECRET=" + "cd" * 32 + "\n"
        "KY_MATRIX_SERVER_NAME=example.invalid\nKY_MATRIX_HOST=https://matrix.example.invalid\n"
        "KY_MATRIX_CHAT_HOST=https://chat.example.invalid\nKY_MATRIX_RTC_HOST=https://sfu.example.invalid\n"
        "KY_MATRIX_MEDIA_BIND_IP=192.0.2.10\nKY_MATRIX_ADMIN_CLIENT_ID=01J0000000000000000000ADMN\n"
    )
    env = {key: value for key, value in os.environ.items() if not key.startswith(("COMPOSE_", "KY_", "GODEBUG"))}

    def render(files):
        command = ["docker", "compose", "--project-name", "kymessages-profile-check", "--project-directory", str(root),
                   "--env-file", str(fixture)]
        for file in files:
            command += ["-f", str(root / file)]
        return json.loads(subprocess.check_output(command + ["config", "--format", "json"], env=env))

    for overlays in chains:
        files = ["docker-compose.yml", *overlays]
        baseline = render(files)
        configured = render([*files, "docker-compose.strong-crypto.yml"])
        assert "GODEBUG" not in baseline["services"]["app"].get("environment", {}), files
        assert configured["services"]["app"]["environment"].pop("GODEBUG") == "fips140=on", files
        assert configured == baseline, f"the strong-key overlay changed another setting in {files}"
print(f"All {len(chains)} Compose chains keep every other setting; only the app gains GODEBUG=fips140=on.")
