package com.rendref.netstorage.e2e;

import android.content.ContentProvider;
import android.content.ContentValues;
import android.content.Context;
import android.content.Intent;
import android.database.Cursor;
import android.net.Uri;
import android.os.Bundle;
import android.provider.DocumentsContract;

import java.util.ArrayList;
import java.util.List;

public final class TestDocumentsControlProvider extends ContentProvider {
    public static final String AUTHORITY =
            "com.rendref.netstorage.e2e.documents.control";

    private static final String TARGET_PACKAGE = "com.rendref.netstorage";
    private static final String METHOD_RESET = "reset";
    private static final String METHOD_OPERATIONS = "operations";
    private static final String METHOD_SHA256 = "sha256";
    private static final String METHOD_GRANT = "grant";
    private static final String RESULT_OPERATIONS = "operations";
    private static final String RESULT_SHA256 = "sha256";
    private static final String RESULT_TREE_URI = "tree_uri";
    private static final Uri CONTROL_URI =
            Uri.parse("content://" + AUTHORITY);

    @Override
    public boolean onCreate() {
        return true;
    }

    @Override
    public Bundle call(String method, String arg, Bundle extras) {
        Bundle result = new Bundle();
        switch (method) {
            case METHOD_RESET:
                TestDocumentsProvider.resetForControl();
                return result;
            case METHOD_OPERATIONS:
                ArrayList<String> values = new ArrayList<>();
                for (TestDocumentOperation operation
                        : TestDocumentsProvider.operationsForControl()) {
                    values.add(
                            operation.getKind()
                                    + "\t"
                                    + operation.getRelativePath()
                    );
                }
                result.putStringArrayList(RESULT_OPERATIONS, values);
                return result;
            case METHOD_SHA256:
                if (arg == null) {
                    throw new IllegalArgumentException(
                            "SHA-256 path is required"
                    );
                }
                result.putString(
                        RESULT_SHA256,
                        TestDocumentsProvider.sha256ForControl(arg)
                );
                return result;
            case METHOD_GRANT:
                if (!TARGET_PACKAGE.equals(arg)) {
                    throw new IllegalArgumentException(
                            "Test documents may only be granted to netStorage"
                    );
                }
                TestDocumentsProvider.resetForControl();
                Uri treeUri = DocumentsContract.buildTreeDocumentUri(
                        TestDocumentsProvider.AUTHORITY,
                        TestDocumentsProvider.ROOT_DOCUMENT_ID
                );
                Context providerContext = getContext();
                if (providerContext == null) {
                    throw new IllegalStateException(
                            "Test control provider context is unavailable"
                    );
                }
                providerContext.grantUriPermission(
                        TARGET_PACKAGE,
                        treeUri,
                        Intent.FLAG_GRANT_READ_URI_PERMISSION
                                | Intent.FLAG_GRANT_WRITE_URI_PERMISSION
                                | Intent.FLAG_GRANT_PREFIX_URI_PERMISSION
                                | Intent.FLAG_GRANT_PERSISTABLE_URI_PERMISSION
                );
                result.putString(RESULT_TREE_URI, treeUri.toString());
                return result;
            default:
                throw new IllegalArgumentException(
                        "Unsupported test provider control method: " + method
                );
        }
    }

    @Override
    public Cursor query(
            Uri uri,
            String[] projection,
            String selection,
            String[] selectionArgs,
            String sortOrder
    ) {
        throw new UnsupportedOperationException(
                "Test provider control queries are unsupported"
        );
    }

    @Override
    public String getType(Uri uri) {
        return null;
    }

    @Override
    public Uri insert(Uri uri, ContentValues values) {
        throw new UnsupportedOperationException(
                "Test provider control inserts are unsupported"
        );
    }

    @Override
    public int delete(
            Uri uri,
            String selection,
            String[] selectionArgs
    ) {
        throw new UnsupportedOperationException(
                "Test provider control deletes are unsupported"
        );
    }

    @Override
    public int update(
            Uri uri,
            ContentValues values,
            String selection,
            String[] selectionArgs
    ) {
        throw new UnsupportedOperationException(
                "Test provider control updates are unsupported"
        );
    }

    public static void reset(Context context) {
        Bundle result = context.getContentResolver().call(
                CONTROL_URI,
                METHOD_RESET,
                null,
                null
        );
        if (result == null) {
            throw new IllegalStateException(
                    "Unable to reset test documents provider"
            );
        }
    }

    public static List<TestDocumentOperation> operations(Context context) {
        Bundle result = context.getContentResolver().call(
                CONTROL_URI,
                METHOD_OPERATIONS,
                null,
                null
        );
        ArrayList<String> values = result == null
                ? null
                : result.getStringArrayList(RESULT_OPERATIONS);
        if (values == null) {
            throw new IllegalStateException(
                    "Unable to read test document operations"
            );
        }
        ArrayList<TestDocumentOperation> operations =
                new ArrayList<>(values.size());
        for (String value : values) {
            int separator = value.indexOf('\t');
            if (separator <= 0) {
                throw new IllegalStateException(
                        "Invalid test operation record"
                );
            }
            operations.add(
                    new TestDocumentOperation(
                            value.substring(0, separator),
                            value.substring(separator + 1)
                    )
            );
        }
        return operations;
    }

    public static String sha256(Context context, String relativePath) {
        Bundle result = context.getContentResolver().call(
                CONTROL_URI,
                METHOD_SHA256,
                relativePath,
                null
        );
        String digest = result == null
                ? null
                : result.getString(RESULT_SHA256);
        if (digest == null) {
            throw new IllegalStateException("Unable to hash test document");
        }
        return digest;
    }

    public static Uri grantTreeAccess(
            Context context,
            String targetPackage
    ) {
        Bundle result = context.getContentResolver().call(
                CONTROL_URI,
                METHOD_GRANT,
                targetPackage,
                null
        );
        String treeUri = result == null
                ? null
                : result.getString(RESULT_TREE_URI);
        if (treeUri == null) {
            throw new IllegalStateException(
                    "Unable to grant test documents tree"
            );
        }
        return Uri.parse(treeUri);
    }
}
