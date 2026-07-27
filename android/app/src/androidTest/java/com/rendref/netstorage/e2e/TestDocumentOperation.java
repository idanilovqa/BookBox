package com.rendref.netstorage.e2e;

public final class TestDocumentOperation {
    private final String kind;
    private final String relativePath;

    public TestDocumentOperation(String kind, String relativePath) {
        this.kind = kind;
        this.relativePath = relativePath;
    }

    public String getKind() {
        return kind;
    }

    public String getRelativePath() {
        return relativePath;
    }
}
