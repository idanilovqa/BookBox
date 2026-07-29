package com.rendref.bookbox.feature.folders

internal fun hasSearchCharacters(query: CharSequence): Boolean {
    var index = 0
    while (index < query.length) {
        val codePoint = Character.codePointAt(query, index)
        if (Character.isLetterOrDigit(codePoint)) {
            return true
        }
        index += Character.charCount(codePoint)
    }
    return false
}
