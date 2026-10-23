"""Python client for Homelab's Files, Secrets and Databases services. Standard library only."""

from .client import Homelab
from .errors import HomelabError
from .db import Db
from .files import Files
from .secrets import Secrets
from .types import DbInfo, DbQueryResult, DbRows, DbTable, File, FileList, Folder, Gps, Meta, SecretInfo, SecretSaved, SecretVersion, UploadResult

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
    "Meta",
    "SecretInfo",
    "SecretSaved",
    "SecretVersion",
    "Secrets",
    "UploadResult",
    "__version__",
]
