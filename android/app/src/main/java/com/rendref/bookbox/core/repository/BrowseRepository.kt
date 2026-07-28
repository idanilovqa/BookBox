package com.rendref.bookbox.core.repository

import com.rendref.bookbox.core.model.BookFilesDto
import com.rendref.bookbox.core.model.BrowseDto
import com.rendref.bookbox.core.model.RootDto
import com.rendref.bookbox.core.network.BrowseApi
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.flow

fun interface RootSource {
    fun roots(): Flow<List<RootDto>>
}

interface BrowseSource : RootSource {
    fun list(rootId: String, path: String): Flow<BrowseDto>

    fun bookFiles(bookId: String): Flow<BookFilesDto>
}

class BrowseRepository(
    private val api: BrowseApi,
) : BrowseSource {
    override fun roots(): Flow<List<RootDto>> = flow {
        emit(api.roots())
    }

    override fun list(rootId: String, path: String): Flow<BrowseDto> = flow {
        emit(api.list(rootId, path))
    }

    override fun bookFiles(bookId: String): Flow<BookFilesDto> = flow {
        emit(api.bookFiles(bookId))
    }
}
