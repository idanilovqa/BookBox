# BookBox Catalog

BookBox catalogs audiobook recordings found under configured library roots and exposes them without changing the source library.

## Language

**Audiobook**:
A cataloged recording represented by one inferred book and its assigned audio files. A folder-based recording and a loose audio file each represent one audiobook.

**Raw library entity**:
A physical folder or recognized audiobook-related file under a configured library root. Raw library entities retain their source-relative paths.

**Audiobook-related content**:
Recognized audio files, recognized cover images, and folders that contain or are ancestors of those files. Empty folders and unrelated files do not belong to this set.

**Duplicate recording**:
Two or more audiobook copies with the same normalized title, author identity, and narrator identity whose total durations fall within the accepted tolerance. File format, bitrate, and source location do not distinguish copies.
_Avoid_: Duplicate title

**Duplicate group**:
The set of audiobook copies classified as one duplicate recording. Each member remains an independent audiobook that a user can open or transfer.
