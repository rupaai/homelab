"""Python client for Homelab's Files service. Standard library only."""

from .client import Homelab
from .errors import HomelabError
from .files import Files
from .types import File, FileList, Folder, Gps, Meta, UploadResult

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
    "UploadResult",
    "__version__",
]
