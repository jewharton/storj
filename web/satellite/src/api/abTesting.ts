// Copyright (C) 2021 Storj Labs, Inc.
// See LICENSE for copying information.

import { ErrorUnauthorized } from '@/api/errors/ErrorUnauthorized';
import { PassphraseEntryInfo } from '@/types/abTesting';
import { HttpClient } from '@/utils/httpClient';

/**
 * ABHttpApi is a console AB testing API.
 * Exposes all ab-testing related functionality
 */
export class ABHttpApi {
    private readonly http: HttpClient = new HttpClient();
    private readonly ROOT_PATH: string = '/api/v0/ab';

    /**
     * Used to get information regarding the display of the
     * passphrase entry screen.
     *
     * @throws Error
     */
    public async getPassphraseEntryRequired(): Promise<PassphraseEntryInfo> {
        const path = `${this.ROOT_PATH}/passphrase-entry-required`;
        const response = await this.http.get(path);
        if (response.ok) {
            const abResponse = await response.json();

            return new PassphraseEntryInfo(
                abResponse.required,
            );
        }

        if (response.status === 401) {
            throw new ErrorUnauthorized();
        }

        throw new Error('cannot get ab testing data');
    }
}
