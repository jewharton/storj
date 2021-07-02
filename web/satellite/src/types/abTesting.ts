// Copyright (C) 2021 Storj Labs, Inc.
// See LICENSE for copying information.

/**
 * Exposes all ab-testing-related functionality.
 */
export interface ABApi {
    /**
     * Used to get information regarding the display of the
     * passphrase entry screen.
     *
     * @throws Error
     */
    getPassphraseEntryRequired(): Promise<PassphraseEntryInfo>;
}

/**
 * PassphraseEntryInfo class holds information regarding
 * the display of the passphrase entry screen.
 */
export class PassphraseEntryInfo {
    public constructor(
        public required: boolean = true,
    ) {}
}
