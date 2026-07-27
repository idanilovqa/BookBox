package com.rendref.netstorage.e2e;

import android.database.Cursor;
import android.database.MatrixCursor;
import android.os.CancellationSignal;
import android.os.Handler;
import android.os.Looper;
import android.os.ParcelFileDescriptor;
import android.os.ProxyFileDescriptorCallback;
import android.os.storage.StorageManager;
import android.provider.DocumentsContract;
import android.provider.DocumentsProvider;
import android.system.ErrnoException;
import android.system.OsConstants;

import java.io.FileNotFoundException;
import java.io.IOException;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Comparator;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

public final class TestDocumentsProvider extends DocumentsProvider {
    public static final String AUTHORITY = "com.rendref.netstorage.e2e.documents";
    public static final String ROOT_DOCUMENT_ID = "root";

    private static final String ROOT_ID = "netstorage-e2e-root";
    private static final String DOCUMENT_PREFIX = "document:";
    private static final Object LOCK = new Object();
    private static final LinkedHashMap<String, MemoryDocument> DOCUMENTS =
            new LinkedHashMap<>();
    private static final ArrayList<TestDocumentOperation> OPERATIONS =
            new ArrayList<>();
    private static long nextDocumentID = 1L;

    private static final String[] ROOT_PROJECTION = {
            DocumentsContract.Root.COLUMN_ROOT_ID,
            DocumentsContract.Root.COLUMN_DOCUMENT_ID,
            DocumentsContract.Root.COLUMN_TITLE,
            DocumentsContract.Root.COLUMN_FLAGS,
            DocumentsContract.Root.COLUMN_MIME_TYPES,
    };
    private static final String[] DOCUMENT_PROJECTION = {
            DocumentsContract.Document.COLUMN_DOCUMENT_ID,
            DocumentsContract.Document.COLUMN_DISPLAY_NAME,
            DocumentsContract.Document.COLUMN_MIME_TYPE,
            DocumentsContract.Document.COLUMN_SIZE,
            DocumentsContract.Document.COLUMN_FLAGS,
    };

    @Override
    public boolean onCreate() {
        resetForControl();
        return true;
    }

    @Override
    public Cursor queryRoots(String[] projection) {
        MatrixCursor cursor = new MatrixCursor(
                projection == null ? ROOT_PROJECTION : projection
        );
        Map<String, Object> values = new LinkedHashMap<>();
        values.put(DocumentsContract.Root.COLUMN_ROOT_ID, ROOT_ID);
        values.put(
                DocumentsContract.Root.COLUMN_DOCUMENT_ID,
                ROOT_DOCUMENT_ID
        );
        values.put(DocumentsContract.Root.COLUMN_TITLE, "netStorage E2E");
        values.put(
                DocumentsContract.Root.COLUMN_FLAGS,
                DocumentsContract.Root.FLAG_SUPPORTS_CREATE
        );
        values.put(DocumentsContract.Root.COLUMN_MIME_TYPES, "*/*");
        addRow(cursor, values);
        return cursor;
    }

    @Override
    public Cursor queryDocument(String documentId, String[] projection)
            throws FileNotFoundException {
        MatrixCursor cursor = new MatrixCursor(
                projection == null ? DOCUMENT_PROJECTION : projection
        );
        synchronized (LOCK) {
            includeDocument(cursor, requireDocument(documentId));
        }
        return cursor;
    }

    @Override
    public Cursor queryChildDocuments(
            String parentDocumentId,
            String[] projection,
            String sortOrder
    ) throws FileNotFoundException {
        MatrixCursor cursor = new MatrixCursor(
                projection == null ? DOCUMENT_PROJECTION : projection
        );
        synchronized (LOCK) {
            MemoryDocument parent = requireDocument(parentDocumentId);
            if (!parent.directory) {
                throw new FileNotFoundException("Parent document is missing");
            }
            DOCUMENTS.values().stream()
                    .filter(document -> parentDocumentId.equals(document.parentID))
                    .sorted(Comparator.comparing(
                            document -> document.name,
                            String.CASE_INSENSITIVE_ORDER
                    ))
                    .forEach(document -> includeDocument(cursor, document));
        }
        return cursor;
    }

    @Override
    public boolean isChildDocument(
            String parentDocumentId,
            String documentId
    ) {
        synchronized (LOCK) {
            if (parentDocumentId == null
                    || documentId == null
                    || parentDocumentId.equals(documentId)
                    || !DOCUMENTS.containsKey(parentDocumentId)) {
                return false;
            }
            MemoryDocument current = DOCUMENTS.get(documentId);
            int remaining = DOCUMENTS.size();
            while (current != null
                    && current.parentID != null
                    && remaining-- > 0) {
                if (parentDocumentId.equals(current.parentID)) {
                    return true;
                }
                current = DOCUMENTS.get(current.parentID);
            }
            return false;
        }
    }

    @Override
    public ParcelFileDescriptor openDocument(
            String documentId,
            String mode,
            CancellationSignal signal
    ) throws FileNotFoundException {
        synchronized (LOCK) {
            MemoryDocument document = requireDocument(documentId);
            if (document.directory) {
                throw new FileNotFoundException("Document is missing");
            }
            if (mode.indexOf('t') >= 0) {
                document.content = new byte[0];
            }
            recordOperation("open", relativePath(document));
        }
        StorageManager storageManager = getContext() == null
                ? null
                : getContext().getSystemService(StorageManager.class);
        if (storageManager == null) {
            throw new FileNotFoundException(
                    "Android proxy descriptor service is unavailable"
            );
        }
        try {
            return storageManager.openProxyFileDescriptor(
                    ParcelFileDescriptor.parseMode(mode),
                    new MemoryDocumentCallback(documentId),
                    new Handler(Looper.getMainLooper())
            );
        } catch (IOException failure) {
            FileNotFoundException wrapped = new FileNotFoundException(
                    "Unable to open in-memory test document"
            );
            wrapped.initCause(failure);
            throw wrapped;
        }
    }

    @Override
    public String createDocument(
            String parentDocumentId,
            String mimeType,
            String displayName
    ) throws FileNotFoundException {
        synchronized (LOCK) {
            validateDisplayName(displayName);
            MemoryDocument parent = requireDocument(parentDocumentId);
            if (!parent.directory) {
                throw new FileNotFoundException("Parent document is missing");
            }
            for (MemoryDocument document : DOCUMENTS.values()) {
                if (parentDocumentId.equals(document.parentID)
                        && displayName.equals(document.name)) {
                    throw new FileNotFoundException(
                            "Document already exists"
                    );
                }
            }
            String id = DOCUMENT_PREFIX + nextDocumentID++;
            MemoryDocument created = new MemoryDocument(
                    id,
                    parentDocumentId,
                    displayName,
                    DocumentsContract.Document.MIME_TYPE_DIR.equals(mimeType)
            );
            DOCUMENTS.put(id, created);
            recordOperation("create", relativePath(created));
            return id;
        }
    }

    @Override
    public String renameDocument(String documentId, String displayName)
            throws FileNotFoundException {
        synchronized (LOCK) {
            validateDisplayName(displayName);
            MemoryDocument document = requireDocument(documentId);
            if (ROOT_DOCUMENT_ID.equals(documentId)) {
                throw new FileNotFoundException("Document is missing");
            }
            for (MemoryDocument candidate : DOCUMENTS.values()) {
                if (!candidate.id.equals(documentId)
                        && equalsNullable(candidate.parentID, document.parentID)
                        && displayName.equals(candidate.name)) {
                    throw new FileNotFoundException(
                            "Document already exists"
                    );
                }
            }
            document.name = displayName;
            recordOperation("rename", relativePath(document));
            return document.id;
        }
    }

    @Override
    public void deleteDocument(String documentId)
            throws FileNotFoundException {
        synchronized (LOCK) {
            MemoryDocument document = requireDocument(documentId);
            if (ROOT_DOCUMENT_ID.equals(documentId)) {
                throw new FileNotFoundException("Document is missing");
            }
            String path = relativePath(document);
            ArrayDeque<String> pending = new ArrayDeque<>();
            ArrayList<String> deleting = new ArrayList<>();
            pending.add(documentId);
            while (!pending.isEmpty()) {
                String id = pending.removeFirst();
                deleting.add(id);
                for (MemoryDocument child : DOCUMENTS.values()) {
                    if (id.equals(child.parentID)) {
                        pending.add(child.id);
                    }
                }
            }
            for (String id : deleting) {
                DOCUMENTS.remove(id);
            }
            recordOperation("delete", path);
        }
    }

    static void resetForControl() {
        synchronized (LOCK) {
            DOCUMENTS.clear();
            DOCUMENTS.put(
                    ROOT_DOCUMENT_ID,
                    new MemoryDocument(
                            ROOT_DOCUMENT_ID,
                            null,
                            "netStorage E2E",
                            true
                    )
            );
            OPERATIONS.clear();
            nextDocumentID = 1L;
        }
    }

    static List<TestDocumentOperation> operationsForControl() {
        synchronized (LOCK) {
            return new ArrayList<>(OPERATIONS);
        }
    }

    static String sha256ForControl(String path) {
        byte[] bytes;
        synchronized (LOCK) {
            MemoryDocument document = documentForPath(path);
            if (document.directory) {
                throw new IllegalStateException("Test document is missing");
            }
            bytes = Arrays.copyOf(document.content, document.content.length);
        }
        try {
            byte[] digest = MessageDigest.getInstance("SHA-256").digest(bytes);
            StringBuilder encoded = new StringBuilder(digest.length * 2);
            for (byte value : digest) {
                encoded.append(String.format("%02x", value & 0xff));
            }
            return encoded.toString();
        } catch (NoSuchAlgorithmException impossible) {
            throw new IllegalStateException(impossible);
        }
    }

    private static void includeDocument(
            MatrixCursor cursor,
            MemoryDocument document
    ) {
        String mimeType = document.directory
                ? DocumentsContract.Document.MIME_TYPE_DIR
                : "application/octet-stream";
        int flags = document.directory
                ? DocumentsContract.Document.FLAG_DIR_SUPPORTS_CREATE
                    | DocumentsContract.Document.FLAG_SUPPORTS_DELETE
                    | DocumentsContract.Document.FLAG_SUPPORTS_RENAME
                : DocumentsContract.Document.FLAG_SUPPORTS_WRITE
                    | DocumentsContract.Document.FLAG_SUPPORTS_DELETE
                    | DocumentsContract.Document.FLAG_SUPPORTS_RENAME;
        Map<String, Object> values = new LinkedHashMap<>();
        values.put(DocumentsContract.Document.COLUMN_DOCUMENT_ID, document.id);
        values.put(
                DocumentsContract.Document.COLUMN_DISPLAY_NAME,
                document.name
        );
        values.put(DocumentsContract.Document.COLUMN_MIME_TYPE, mimeType);
        values.put(
                DocumentsContract.Document.COLUMN_SIZE,
                (long) document.content.length
        );
        values.put(DocumentsContract.Document.COLUMN_FLAGS, flags);
        addRow(cursor, values);
    }

    private static void addRow(
            MatrixCursor cursor,
            Map<String, Object> values
    ) {
        MatrixCursor.RowBuilder row = cursor.newRow();
        for (String column : cursor.getColumnNames()) {
            row.add(column, values.get(column));
        }
    }

    private static MemoryDocument requireDocument(String documentId)
            throws FileNotFoundException {
        MemoryDocument document = DOCUMENTS.get(documentId);
        if (document == null) {
            throw new FileNotFoundException("Document is missing");
        }
        return document;
    }

    private static MemoryDocument documentForPath(String path) {
        List<String> segments = pathSegments(path);
        MemoryDocument current = DOCUMENTS.get(ROOT_DOCUMENT_ID);
        if (current == null) {
            throw new IllegalStateException("Test document root is missing");
        }
        for (String segment : segments) {
            MemoryDocument match = null;
            for (MemoryDocument document : DOCUMENTS.values()) {
                if (current.id.equals(document.parentID)
                        && segment.equals(document.name)) {
                    if (match != null) {
                        throw new IllegalStateException(
                                "Test document path is ambiguous"
                        );
                    }
                    match = document;
                }
            }
            if (match == null) {
                throw new IllegalStateException("Test document is missing");
            }
            current = match;
        }
        return current;
    }

    private static List<String> pathSegments(String path) {
        if (path == null || path.isEmpty() || path.startsWith("/")
                || path.indexOf('\\') >= 0) {
            throw new IllegalStateException(
                    "Test document path is invalid"
            );
        }
        String[] values = path.split("/", -1);
        ArrayList<String> segments = new ArrayList<>(values.length);
        for (String value : values) {
            if (value.isEmpty() || ".".equals(value) || "..".equals(value)
                    || containsControl(value)) {
                throw new IllegalStateException(
                        "Test document path is invalid"
                );
            }
            segments.add(value);
        }
        return segments;
    }

    private static String relativePath(MemoryDocument document)
            throws FileNotFoundException {
        if (ROOT_DOCUMENT_ID.equals(document.id)) {
            return "";
        }
        ArrayDeque<String> segments = new ArrayDeque<>();
        MemoryDocument current = document;
        while (!ROOT_DOCUMENT_ID.equals(current.id)) {
            segments.addFirst(current.name);
            current = DOCUMENTS.get(current.parentID);
            if (current == null) {
                throw new FileNotFoundException(
                        "Document escaped test root"
                );
            }
        }
        return String.join("/", segments);
    }

    private static void validateDisplayName(String displayName)
            throws FileNotFoundException {
        if (displayName == null || displayName.trim().isEmpty()
                || ".".equals(displayName) || "..".equals(displayName)
                || displayName.indexOf('/') >= 0
                || displayName.indexOf('\\') >= 0
                || containsControl(displayName)) {
            throw new FileNotFoundException("Display name is invalid");
        }
    }

    private static boolean containsControl(String value) {
        for (int index = 0; index < value.length(); index++) {
            if (Character.isISOControl(value.charAt(index))) {
                return true;
            }
        }
        return false;
    }

    private static boolean equalsNullable(String left, String right) {
        return left == null ? right == null : left.equals(right);
    }

    private static void recordOperation(String kind, String path) {
        OPERATIONS.add(new TestDocumentOperation(kind, path));
    }

    private final class MemoryDocumentCallback
            extends ProxyFileDescriptorCallback {
        private final String documentID;

        private MemoryDocumentCallback(String documentID) {
            this.documentID = documentID;
        }

        @Override
        public long onGetSize() throws ErrnoException {
            synchronized (LOCK) {
                return requireMemoryFile().content.length;
            }
        }

        @Override
        public int onRead(
                long offset,
                int size,
                byte[] data
        ) throws ErrnoException {
            synchronized (LOCK) {
                byte[] content = requireMemoryFile().content;
                if (offset >= content.length) {
                    return 0;
                }
                int start = (int) offset;
                int count = Math.min(size, content.length - start);
                System.arraycopy(content, start, data, 0, count);
                return count;
            }
        }

        @Override
        public int onWrite(
                long offset,
                int size,
                byte[] data
        ) throws ErrnoException {
            synchronized (LOCK) {
                MemoryDocument document = requireMemoryFile();
                long end = offset + size;
                if (offset < 0 || end < offset || end > Integer.MAX_VALUE) {
                    throw new ErrnoException("write", OsConstants.EFBIG);
                }
                if (end > document.content.length) {
                    document.content = Arrays.copyOf(
                            document.content,
                            (int) end
                    );
                }
                System.arraycopy(data, 0, document.content, (int) offset, size);
                return size;
            }
        }

        @Override
        public void onFsync() {
        }

        @Override
        public void onRelease() {
        }

        private MemoryDocument requireMemoryFile()
                throws ErrnoException {
            MemoryDocument document = DOCUMENTS.get(documentID);
            if (document == null) {
                throw new ErrnoException("document", OsConstants.ENOENT);
            }
            if (document.directory) {
                throw new ErrnoException("document", OsConstants.EISDIR);
            }
            return document;
        }
    }

    private static final class MemoryDocument {
        private final String id;
        private final String parentID;
        private String name;
        private final boolean directory;
        private byte[] content = new byte[0];

        private MemoryDocument(
                String id,
                String parentID,
                String name,
                boolean directory
        ) {
            this.id = id;
            this.parentID = parentID;
            this.name = name;
            this.directory = directory;
        }
    }
}
