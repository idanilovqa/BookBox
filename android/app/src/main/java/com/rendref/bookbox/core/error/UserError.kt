package com.rendref.bookbox.core.error

enum class UserRecoveryAction {
    Retry,
    SignIn,
    RetryLater,
    Rescan,
    ChooseDestination,
    FreeSpace,
    RetryTransfer,
    RetryMetadata,
    ReviewMatches,
    RetryCover,
}

sealed class UserError(
    val message: String,
    val recovery: UserRecoveryAction,
) {
    data object ServerUnavailable : UserError(
        message = "The saved server is unavailable.",
        recovery = UserRecoveryAction.Retry,
    )

    data object SessionExpired : UserError(
        message = "Your sign-in has expired.",
        recovery = UserRecoveryAction.SignIn,
    )

    data object Throttled : UserError(
        message = "The server asked the app to wait before trying again.",
        recovery = UserRecoveryAction.RetryLater,
    )

    data object SourceChanged : UserError(
        message = "The source audiobook changed since this copy was prepared.",
        recovery = UserRecoveryAction.Rescan,
    )

    data object SourceMissing : UserError(
        message = "The source audiobook is no longer available.",
        recovery = UserRecoveryAction.Rescan,
    )

    data object DestinationPermissionRevoked : UserError(
        message = "Choose the Android destination again to restore access.",
        recovery = UserRecoveryAction.ChooseDestination,
    )

    data object InsufficientSpace : UserError(
        message = "The Android destination does not have enough free space.",
        recovery = UserRecoveryAction.FreeSpace,
    )

    data object DigestMismatch : UserError(
        message = "The copied file could not be verified.",
        recovery = UserRecoveryAction.RetryTransfer,
    )

    data object ProviderFailure : UserError(
        message = "Audiobook metadata could not be loaded.",
        recovery = UserRecoveryAction.RetryMetadata,
    )

    data object AmbiguousMetadata : UserError(
        message = "Several audiobook matches need your review.",
        recovery = UserRecoveryAction.ReviewMatches,
    )

    data object CoverFailure : UserError(
        message = "The cover could not be published.",
        recovery = UserRecoveryAction.RetryCover,
    )
}
