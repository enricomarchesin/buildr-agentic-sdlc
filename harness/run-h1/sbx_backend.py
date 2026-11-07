"""The workspace of an H1 run: the Docker Sandbox that `cage.sh` made, reached with `sbx exec`.

H0 runs its agent inside this sandbox. H1 runs the loop on the host and only the tools reach in,
so the sandbox, its image and its network rules are H0's, byte for byte, and the loop is the one
thing that differs.
"""

from __future__ import annotations

from collections.abc import Mapping, Sequence

from cli_backend import CliBackend

__all__ = ('SbxBackend',)


class SbxBackend(CliBackend):
    """Run commands in an existing Docker Sandbox, from the directory the repository is mounted at."""

    provider = 'sbx'
    tool = 'sbx'
    failed_codes = (125,)
    """`sbx exec` exits 125 when it could not run the command, such as when the sandbox does not exist."""
    client_env = {'SBX_NO_TELEMETRY': '1'}

    def command_line(self, workdir: str, env: Mapping[str, str], inner: Sequence[str]) -> list[str]:
        flags = [arg for name, value in env.items() for arg in ('-e', f'{name}={value}')]
        return [self._tool, 'exec', '-w', workdir, *flags, self._name, *inner]
