"""Python client for Homelab's Files and Secrets services. Standard library only."""

from .client import Homelab
from .errors import HomelabError
from .files import Files
from .secrets import Secrets
from .types import File, FileList, Folder, Gps, Meta, SecretInfo, SecretSaved, SecretVersion, UploadResult

__version__ = "0.1.0"

__all__ = [
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
