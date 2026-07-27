package com.rendref.netstorage.core.storage

import android.content.Context
import androidx.datastore.preferences.preferencesDataStore

/**
 * The single process-wide DataStore delegate used by both the foreground app and
 * TransferJobService. Declaring a second delegate for this file is not supported.
 */
internal val Context.netStorageConnectionDataStore by preferencesDataStore(
    name = "netstorage_connection",
)
