// Copyright (C) 2019 Storj Labs, Inc.
// See LICENSE for copying information.

<template>
    <input
        ref="input"
        v-model="searchQuery"
        class="common-search-input"
        :placeholder="`Search ${placeholder}`"
        :style="style"
        type="text"
        autocomplete="off"
        @mouseenter="onMouseEnter"
        @mouseleave="onMouseLeave"
        @input="processSearchQuery"
    >
</template>

<script lang="ts">
import { Component, Prop, Vue } from 'vue-property-decorator';

declare type searchCallback = (search: string) => Promise<void>;
declare interface SearchStyle {
    width: string;
}

@Component
export default class VSearch extends Vue {
    @Prop({default: ''})
    private readonly placeholder: string;
    @Prop({default: () => ''})
    private readonly search: searchCallback;

    private inputWidth = '56px';
    private searchQuery = '';

    public $refs!: {
        input: HTMLElement;
    };

    public get style(): SearchStyle {
        return { width: this.inputWidth };
    }

    public get searchString(): string {
        return this.searchQuery;
    }

    /**
     * Expands search input.
     */
    public onMouseEnter(): void {
        this.inputWidth = '540px';

        this.$refs.input.focus();
    }

    /**
     * Collapses search input if no search query.
     */
    public onMouseLeave(): void {
        if (!this.searchQuery) {
            this.inputWidth = '56px';
            this.$refs.input.blur();
        }
    }

    /**
     * Clears search query and collapses input.
     */
    public clearSearch(): void {
        this.searchQuery = '';
        this.processSearchQuery();
        this.inputWidth = '56px';
    }

    public async processSearchQuery(): Promise<void> {
        await this.search(this.searchQuery);
    }
}
</script>

<style scoped lang="scss">
    .common-search-input {
        position: absolute;
        right: 0;
        bottom: 0;
        padding: 0 38px 0 18px;
        border: 1px solid #f2f2f2;
        box-sizing: border-box;
        box-shadow: 0 4px 4px rgba(231, 232, 238, 0.6);
        outline: none;
        border-radius: 36px;
        height: 56px;
        font-family: 'font_regular', sans-serif;
        font-size: 16px;
        transition: all 0.4s ease-in-out;
        background-image: url('../../../static/images/common/search.png');
        background-repeat: no-repeat;
        background-size: 22px 22px;
        background-position: top 16px right 16px;
    }

    ::-webkit-input-placeholder {
        color: #afb7c1;
    }
</style>
