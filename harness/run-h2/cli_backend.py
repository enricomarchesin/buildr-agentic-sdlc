"""A workspace backend that runs every command through a sandbox's own command line.

The sandbox is made by the cage, with its network rules and checks, before any session starts.
A backend only attaches to it by name and runs commands in it; it creates nothing and removes
nothing. It has no file methods of its own, so the framework reads and writes files with the
shell, in the sandbox, which is where the repository is.

A subclass says how its tool runs a command: `command_line`. The contract is the framework's
(`pydantic_ai.workspaces.protocol`): a non-zero exit is a result, a missing program exits 127,
a timeout raises `WorkspaceTimeoutError`, a sandbox that is gone raises `WorkspaceUnavailableError`.
"""

from __future__ import annotations

import math
import os
import posixpath
import re
import secrets
import shutil
from collections.abc import Mapping, Sequence

import anyio
from pydantic_ai.workspaces import (
    CommandResult,
    LocalWorkspaceBackend,
    SupportsCommands,
    WorkspaceBackend,
    WorkspaceCommand,
    WorkspaceOutputLimitError,
    WorkspaceRef,
    WorkspaceTimeoutError,
    WorkspaceUnavailableError,
)

__all__ = ('CliBackend',)

_ENV_NAME = re.compile(r'[A-Za-z_][A-Za-z0-9_]*')

_PID_DIR = '/tmp/.h2'
_RECORD = f'''mkdir -p {_PID_DIR} && echo $$ > "$0" || exit
mark=$1; shift
printf '%s' "$mark"
"$@"
status=$?
printf '%s' "$mark"
exit $status
'''
"""Runs as `sh -c RECORD PIDFILE MARK argv...`: records its PID, then runs the command between two copies of MARK
on standard output.

The marks are there because the tool's own client writes to the same standard output as the command: once a day
`sbx` checks for a newer version of itself, and the notice it prints when it finds one landed in the middle of a
command's output, which the framework expected to be base64 (a review was lost to it on 17 November 2026). What
lies outside the marks is the tool's, not the command's, and goes to standard error. The shell stays the command's
parent instead of becoming it, so the recorded PID is the shell's, and stopping it stops its children."""
_STOP = '''
kill_tree() { for c in $(pgrep -P "$1" 2> /dev/null); do kill_tree "$c"; done; kill -KILL "$1" 2> /dev/null; }
read -r pid 2> /dev/null < "$0" || exit 0
[ -n "$pid" ] || exit 0
kill_tree "$pid"
rm -f "$0"
'''
"""Runs as `sh -c STOP PIDFILE`: kills the recorded command and everything it started.

Killing the local client does not stop a command running in the sandbox, so a timeout or a
cancellation sends this second command to do it.
"""


def command_argv(command: WorkspaceCommand, shell: bool) -> list[str]:
    if shell:
        if not isinstance(command, str):
            raise TypeError('with shell=True, command must be a string')
        return ['sh', '-c', command]
    if isinstance(command, str):
        raise TypeError('without shell=True, command must be a sequence of arguments')
    return list(command)


def unframed(stdout: str, mark: str) -> tuple[str, str]:
    """The command's own output between the two marks, and what the tool wrote around them.

    No mark: the command never started, so all of it is the tool's message. One mark: the command was cut short,
    and everything after it is the command's.
    """
    start = stdout.find(mark)
    if start < 0:
        return '', stdout
    end = stdout.rfind(mark)
    if end == start:
        return stdout[start + len(mark) :], stdout[:start]
    return stdout[start + len(mark) : end], stdout[:start] + stdout[end + len(mark) :]


def checked_env(env: Mapping[str, str]) -> dict[str, str]:
    for name in env:
        if not _ENV_NAME.fullmatch(name):
            raise ValueError(f'invalid environment variable name: {name!r}')
    return dict(env)


class CliBackend(WorkspaceBackend, SupportsCommands):
    """Run commands in a sandbox that already exists, through its command line tool.

    Args:
        name: The sandbox's name.
        working_dir: The absolute directory in the sandbox where commands start.
        env: Environment variables for every command; the per-call `env` goes on top.
    """

    provider = 'cli'
    tool = ''
    """The command line tool, found on `PATH`."""
    failed_codes: tuple[int, ...] = ()
    """Exit codes by which the tool says it could not run the command at all."""
    client_env: Mapping[str, str] = {}
    """Extra environment for the tool itself, not for the command."""
    stop_timeout = 5.0
    """Bounds how long stopping a command may take when the tool stops answering."""

    def __init__(self, name: str, *, working_dir: str, env: Mapping[str, str] | None = None):
        if not name:
            raise ValueError('name must be the sandbox name')
        if not posixpath.isabs(working_dir):
            raise ValueError(f'working_dir must be an absolute path in the sandbox, got {working_dir!r}')
        self._name = name
        self._working_dir = posixpath.normpath(working_dir)
        self._env = checked_env(env or {})
        self._ref = WorkspaceRef(provider=self.provider, id=name)
        tool = shutil.which(self.tool)
        self._tool = os.path.abspath(tool) if tool else self.tool
        self._runner = LocalWorkspaceBackend('/', env=self.client_env)
        self._lock = anyio.Lock()
        self._resolved: str | None = None

    @property
    def ref(self) -> WorkspaceRef:
        return self._ref

    @property
    def name(self) -> str:
        return self._name

    def command_line(self, workdir: str, env: Mapping[str, str], inner: Sequence[str]) -> list[str]:
        """The tool's own command line that runs `inner` in the sandbox, from `workdir`, with `env`."""
        raise NotImplementedError

    async def working_dir(self) -> str:
        async with self._lock:
            if self._resolved is None:
                result = await self._exec(
                    ['sh', '-c', 'cd "$1" && pwd -P', 'sh', self._working_dir], env={}, timeout=None, workdir='/'
                )
                printed = result.stdout.removesuffix('\n')
                if result.exit_code != 0 or not posixpath.isabs(printed):
                    raise WorkspaceUnavailableError(
                        f'cannot enter {self._working_dir} in sandbox {self._name}: '
                        f'exit code {result.exit_code}: {result.stderr.strip() or result.stdout!r}'
                    )
                self._resolved = printed
            return self._resolved

    async def run(
        self,
        command: WorkspaceCommand,
        *,
        shell: bool = False,
        env: Mapping[str, str] | None = None,
        timeout: float | None = None,
    ) -> CommandResult:
        if timeout is not None and (
            not isinstance(timeout, (int, float)) or not math.isfinite(timeout) or timeout <= 0
        ):
            raise ValueError('timeout must be a positive finite number or None')
        argv = command_argv(command, shell)
        merged = {**self._env, **checked_env(env or {})}
        return await self._exec(argv, env=merged, timeout=timeout, workdir=await self.working_dir())

    async def _exec(
        self, argv: Sequence[str], *, env: Mapping[str, str], timeout: float | None, workdir: str
    ) -> CommandResult:
        pidfile = f'{_PID_DIR}/{secrets.token_hex(6)}.pid'
        mark = f'<<cmd:{secrets.token_hex(8)}>>'
        inner = ['sh', '-c', _RECORD, pidfile, mark, *argv]
        try:
            result = await self._runner.run(self.command_line(workdir, env, inner), timeout=timeout)
        except (anyio.get_cancelled_exc_class(), WorkspaceTimeoutError, WorkspaceOutputLimitError):
            await self._stop(pidfile)
            raise
        stdout, stray = unframed(result.stdout, mark)
        result = CommandResult(exit_code=result.exit_code, stdout=stdout, stderr=stray + result.stderr)
        if self.tool_failed(result):
            raise WorkspaceUnavailableError(
                f'sandbox {self._name} is unavailable: {result.stderr.strip() or result.stdout.strip()}'
            )
        # A command killed by a signal in a live sandbox is a result. The same exit code comes back
        # when the sandbox itself was destroyed during the command, which is not.
        if self.maybe_destroyed(result) and not await self._alive():
            raise WorkspaceUnavailableError(f'sandbox {self._name} was destroyed during the command')
        return result

    def tool_failed(self, result: CommandResult) -> bool:
        """Whether the tool, not the command, failed: the sandbox does not exist or cannot be reached."""
        return result.exit_code in self.failed_codes

    def maybe_destroyed(self, result: CommandResult) -> bool:
        """Whether the result could be what a sandbox destroyed during the command looks like."""
        return result.exit_code >= 128

    async def _alive(self) -> bool:
        probe = await self._runner.run(self.command_line('/', {}, ['true']), timeout=self.stop_timeout)
        return probe.exit_code == 0 and not self.tool_failed(probe)

    async def _stop(self, pidfile: str) -> None:
        # Best effort, and shielded so that a cancelled run still stops its command: a tool that
        # stalls now has nothing to report.
        with anyio.CancelScope(shield=True), anyio.move_on_after(self.stop_timeout):
            try:
                await self._runner.run(
                    self.command_line('/', {}, ['sh', '-c', _STOP, pidfile]), timeout=self.stop_timeout
                )
            except Exception:  # noqa: BLE001
                pass
