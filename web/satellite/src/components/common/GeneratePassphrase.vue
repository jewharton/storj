// Copyright (C) 2021 Storj Labs, Inc.
// See LICENSE for copying information.

<template>
    <div class="generate-container">
        <h1 class="generate-container__title">Encryption Passphrase</h1>
        <div class="generate-container__choosing">
            <p class="generate-container__choosing__label">Passphrase</p>
            <div class="generate-container__choosing__right">
                <p
                    class="generate-container__choosing__right__option left-option"
                    :class="{ active: isGenerateState }"
                    @click="onChooseGenerate"
                >
                    Generate Phrase
                </p>
                <p
                    class="generate-container__choosing__right__option"
                    :class="{ active: isEnterState }"
                    @click="onChooseCreate"
                >
                    Enter Phrase
                </p>
            </div>
        </div>
        <div v-if="isEnterState" class="generate-container__enter-passphrase-box">
            <div class="generate-container__enter-passphrase-box__header">
                <GreenWarningIcon />
                <h2 class="generate-container__enter-passphrase-box__header__label">Enter an Existing Passphrase</h2>
            </div>
            <p class="generate-container__enter-passphrase-box__message">
                if you already have an encryption passphrase, enter your encryption passphrase here.
            </p>
        </div>
        <div class="generate-container__value-area">
            <div v-if="isGenerateState" class="generate-container__value-area__mnemonic">
                <p class="generate-container__value-area__mnemonic__value">{{ passphrase }}</p>
                <VButton
                    class="generate-container__value-area__mnemonic__button"
                    label="Copy"
                    width="66px"
                    height="30px"
                    :on-press="onCopyClick"
                />
            </div>
            <div v-else class="generate-container__value-area__password">
                <HeaderedInput
                    class="generate-container__value-area__password__input"
                    placeholder="Enter encryption passphrase here"
                    :error="errorMessage"
                    @setData="onChangePassphrase"
                />
            </div>
        </div>
        <div v-if="isGenerateState" class="generate-container__warning">
            <h2 class="generate-container__warning__title">Save Your Encryption Passphrase</h2>
            <p class="generate-container__warning__message">
                You’ll need this passphrase to access data in the future. This is the only time it will be displayed.
                Be sure to write it down.
            </p>
            <label class="generate-container__warning__check-area" :class="{ error: isError }" for="pass-checkbox">
                <input
                    id="pass-checkbox"
                    v-model="isChecked"
                    class="generate-container__warning__check-area__checkbox"
                    type="checkbox"
                    @change="isError = false"
                >
                Yes, I wrote this down or saved it somewhere.
            </label>
        </div>
        <VButton
            class="generate-container__next-button"
            label="Next"
            width="100%"
            height="48px"
            :on-press="onProceed"
            :is-disabled="isButtonDisabled"
        />
    </div>
</template>

<script lang="ts">
import * as bip39 from 'bip39';
import { Component, Prop, Vue } from 'vue-property-decorator';

import HeaderedInput from '@/components/common/HeaderedInput.vue';
import VButton from '@/components/common/VButton.vue';

import BackIcon from '@/../static/images/accessGrants/back.svg';
import GreenWarningIcon from '@/../static/images/accessGrants/greenWarning.svg';

import { AnalyticsHttpApi } from '@/api/analytics';
import { AnalyticsEvent } from '@/utils/constants/analyticsEventNames';

@Component({
    components: {
        GreenWarningIcon,
        BackIcon,
        VButton,
        HeaderedInput,
    },
})
export default class GeneratePassphrase extends Vue {
    @Prop({ default: () => null })
    public readonly onButtonClick: () => void;
    @Prop({ default: () => null })
    public readonly setParentPassphrase: (passphrase: string) => void;
    @Prop({ default: false })
    public readonly isLoading: boolean;

    private readonly analytics: AnalyticsHttpApi = new AnalyticsHttpApi();

    public isGenerateState = true;
    public isEnterState = false;
    public isChecked = false;
    public isError = false;
    public passphrase = '';
    public errorMessage = '';

    /**
     * Lifecycle hook after initial render.
     * Generates mnemonic string.
     */
    public mounted(): void {
        this.passphrase = bip39.generateMnemonic();
        this.setParentPassphrase(this.passphrase);
    }

    public onProceed(): void {
        if (!this.passphrase) {
            this.errorMessage = 'Passphrase can\'t be empty';

            return;
        }

        if (!this.isChecked && this.isGenerateState) {
            this.isError = true;

            return;
        }

        this.analytics.eventTriggered(AnalyticsEvent.PASSPHRASE_CREATED);

        this.onButtonClick();
    }

    /**
     * Changes state to generate passphrase.
     */
    public onChooseGenerate(): void {
        if (this.passphrase && this.isGenerateState) return;

        this.passphrase = bip39.generateMnemonic();
        this.setParentPassphrase(this.passphrase);

        this.isEnterState = false;
        this.isGenerateState = true;
    }

    /**
     * Changes state to create passphrase.
     */
    public onChooseCreate(): void {
        if (this.passphrase && this.isEnterState) return;

        this.errorMessage = '';
        this.passphrase = '';
        this.setParentPassphrase(this.passphrase);

        this.isEnterState = true;
        this.isGenerateState = false;
    }

    /**
     * Holds on copy button click logic.
     * Copies passphrase to clipboard.
     */
    public onCopyClick(): void {
        this.$copyText(this.passphrase);
        this.$notify.success('Passphrase was copied successfully');
    }

    /**
     * Changes passphrase data from input value.
     * @param value
     */
    public onChangePassphrase(value: string): void {
        this.passphrase = value.trim();
        this.setParentPassphrase(this.passphrase);
        this.errorMessage = '';
    }

    /**
     * Indicates if button is disabled.
     */
    public get isButtonDisabled(): boolean {
        return this.isLoading || !this.passphrase || (!this.isChecked && this.isGenerateState);
    }
}
</script>

<style scoped lang="scss">
    .generate-container {
        padding: 25px 50px;
        max-width: 515px;
        min-width: 515px;
        font-family: 'font_regular', sans-serif;
        font-style: normal;
        display: flex;
        flex-direction: column;
        align-items: center;
        background-color: #fff;
        border-radius: 6px;

        &__title {
            font-family: 'font_bold', sans-serif;
            font-weight: bold;
            font-size: 22px;
            line-height: 27px;
            color: #000;
            margin: 0 0 30px 0;
        }

        &__enter-passphrase-box {
            padding: 20px;
            background: #f9fffc;
            border: 1px solid #1a9666;
            border-radius: 9px;

            &__header {
                display: flex;
                align-items: center;
                margin-bottom: 10px;

                &__label {
                    font-family: 'font_bold', sans-serif;
                    font-size: 16px;
                    line-height: 19px;
                    color: #1b2533;
                    margin: 0 0 0 10px;
                }
            }

            &__message {
                font-size: 16px;
                line-height: 19px;
                color: #1b2533;
                margin: 0;
            }
        }

        &__warning {
            display: flex;
            flex-direction: column;
            padding: 20px;
            width: calc(100% - 40px);
            margin: 35px 0;
            background: #fff;
            border: 1px solid #e6e9ef;
            border-radius: 9px;

            &__title {
                width: 100%;
                text-align: center;
                font-family: 'font_bold', sans-serif;
                font-size: 16px;
                line-height: 19px;
                color: #1b2533;
                margin: 0 0 0 15px;
            }

            &__message {
                font-size: 16px;
                line-height: 19px;
                color: #1b2533;
                margin: 10px 0 0 0;
                text-align: center;
            }

            &__check-area {
                margin-top: 27px;
                font-size: 14px;
                line-height: 19px;
                color: #1b2533;
                display: flex;
                justify-content: center;
                align-items: center;

                &__checkbox {
                    margin: 0 10px 0 0;
                }
            }
        }

        &__choosing {
            display: flex;
            align-items: center;
            justify-content: space-between;
            width: 100%;
            margin-bottom: 25px;

            &__label {
                font-family: 'font_bold', sans-serif;
                font-size: 16px;
                line-height: 21px;
                color: #354049;
                margin: 0;
            }

            &__right {
                display: flex;
                align-items: center;

                &__option {
                    font-size: 14px;
                    line-height: 17px;
                    color: #768394;
                    margin: 0;
                    cursor: pointer;
                    border-bottom: 3px solid #fff;
                }
            }
        }

        &__value-area {
            width: 100%;
            display: flex;
            align-items: flex-start;

            &__mnemonic {
                display: flex;
                background: #f5f6fa;
                border-radius: 9px;
                padding: 10px;
                width: calc(100% - 20px);

                &__value {
                    font-family: 'Source Code Pro', sans-serif;
                    font-size: 14px;
                    line-height: 25px;
                    color: #384b65;
                    word-break: break-word;
                    margin: 0;
                    word-spacing: 8px;
                }

                &__button {
                    margin-left: 10px;
                    min-width: 66px;
                    min-height: 30px;
                }
            }

            &__password {
                width: 100%;
                margin: 10px 0 20px 0;

                &__input {
                    width: calc(100% - 2px);
                }
            }
        }
    }

    .left-option {
        margin-right: 15px;
    }

    .active {
        font-family: 'font_medium', sans-serif;
        color: #0068dc;
        border-bottom: 3px solid #0068dc;
    }

    .error {
        color: red;
    }

    ::v-deep .label-container {

        &__main {
            margin-bottom: 10px;

            &__label {
                margin: 0;
                font-size: 14px;
                line-height: 19px;
                color: #7c8794;
                font-family: 'font_bold', sans-serif;
            }

            &__error {
                margin: 0 0 0 10px;
                font-size: 14px;
                line-height: 19px;
            }
        }
    }
</style>
