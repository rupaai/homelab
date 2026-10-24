"""Python client for Homelab's Files, Secrets, Databases, Releases and Logs services. Standard library only."""

from .client import Homelab
from .errors import HomelabError
from .db import Db
from .files import Files
from . import logs
from .logs import Logs
from .releases import Releases, version
from .secrets import Secrets
from .types import DbInfo, DbQueryResult, DbRows, DbTable, File, FileList, Folder, Gps, LogLine, LogSearchResult, Meta, Release, SecretInfo, SecretSaved, SecretVersion, UploadResult

__version__ = "0.1.0"

__all__ = [
    "Db",
    "DbInfo",
    "DbQueryResult",
    "DbRows",
    "DbTable",
    "File",
    "FileList",
    "Files",
    "Folder",
    "Gps",
    "Homelab",
    "HomelabError",
    "LogLine",
    "LogSearchResult",
    "Logs",
    "Meta",
    "Release",
    "Releases",
    "SecretInfo",
    "SecretSaved",
    "SecretVersion",
    "Secrets",
    "UploadResult",
    "__version__",
    "logs",
    "version",
]
