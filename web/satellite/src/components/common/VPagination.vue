// Copyright (C) 2019 Storj Labs, Inc.
// See LICENSE for copying information.

<template>
    <div class="pagination-container">
        <div class="pagination-container__pages">
            <div class="pagination-container__button" @click="prevPage">
                <PaginationLeftIcon class="pagination-container__button__image" />
            </div>
            <div class="pagination-container__items">
                <PagesBlock
                    :pages="firstBlockPages"
                    :is-selected="isSelected"
                />
                <span v-if="isFirstDotsShown" class="pages-divider">...</span>
                <PagesBlock
                    :pages="middleBlockPages"
                    :is-selected="isSelected"
                />
                <span v-if="isSecondDotsShown" class="pages-divider">...</span>
                <PagesBlock
                    :pages="lastBlockPages"
                    :is-selected="isSelected"
                />
            </div>
            <div class="pagination-container__button" @click="nextPage">
                <PaginationRightIcon class="pagination-container__button__image" />
            </div>
        </div>
    </div>
</template>

<script lang="ts">
import { Component, Prop, Vue, Watch } from 'vue-property-decorator';

import PagesBlock from '@/components/common/PagesBlock.vue';

import PaginationLeftIcon from '@/../static/images/common/paginationLeft.svg';
import PaginationRightIcon from '@/../static/images/common/paginationRight.svg';

import { OnPageClickCallback, Page } from '@/types/pagination';

@Component({
    components: {
        PagesBlock,
        PaginationLeftIcon,
        PaginationRightIcon,
    },
})
export default class VPagination extends Vue {
    private readonly MAX_PAGES_PER_BLOCK: number = 3;
    private readonly MAX_PAGES_OFF_BLOCKS: number = 6;
    private currentPageNumber = 1;
    public isLoading = false;
    public pagesArray: Page[] = [];
    public firstBlockPages: Page[] = [];
    public middleBlockPages: Page[] = [];
    public lastBlockPages: Page[] = [];

    @Prop({default: 0})
    private readonly totalPageCount: number;
    @Prop({default: () => new Promise(() => false)})
    private readonly onPageClickCallback: OnPageClickCallback;

    /**
     * Component initialization.
     */
    public mounted(): void {
        this.populatePagesArray();
    }

    /**
     * Indicates if current page is first.
     */
    public get isFirstPage(): boolean {
        return this.currentPageNumber === 1;
    }

    /**
     * Indicates if current page is last.
     */
    public get isLastPage(): boolean {
        return this.currentPageNumber === this.totalPageCount;
    }

    /**
     * Indicates if dots after first pages block should appear.
     */
    public get isFirstDotsShown(): boolean {
        return this.middleBlockPages.length <= this.MAX_PAGES_PER_BLOCK
            && this.pagesArray.length > this.MAX_PAGES_OFF_BLOCKS;
    }

    /**
     * Indicates if dots after middle pages block should appear.
     */
    public get isSecondDotsShown(): boolean {
        return !!this.middleBlockPages.length;
    }

    /**
     * Indicates page is current and should appear in different styling.
     */
    public isSelected(page: number): boolean {
        return page === this.currentPageNumber;
    }

    /**
     * Method after total page count change.
     */
    @Watch('totalPageCount')
    public onPageCountChange(_val: number, _oldVal: number): void {
        this.resetPageIndex();
    }

    /**
     * onPageClick fires after concrete page click.
     */
    public async onPageClick(page: number): Promise<void> {
        if (this.isLoading) {
            return;
        }

        this.isLoading = true;
        await this.onPageClickCallback(page);
        this.setCurrentPage(page);
        this.reorganizePageBlocks();
        this.isLoading = false;
    }

    /**
     * nextPage fires after 'next' arrow click.
     */
    public async nextPage(): Promise<void> {
        if (this.isLastPage || this.isLoading) {
            return;
        }

        this.isLoading = true;
        await this.onPageClickCallback(this.currentPageNumber + 1);
        this.incrementCurrentPage();
        this.reorganizePageBlocks();
        this.isLoading = false;
    }

    /**
     * prevPage fires after 'previous' arrow click.
     */
    public async prevPage(): Promise<void> {
        if (this.isFirstPage || this.isLoading) {
            return;
        }

        this.isLoading = true;
        await this.onPageClickCallback(this.currentPageNumber - 1);
        this.decrementCurrentPage();
        this.reorganizePageBlocks();
        this.isLoading = false;
    }

    /**
     * resetPageIndex sets current selected page as first and rebuilds page blocks after.
     */
    public resetPageIndex(): void {
        this.pagesArray = [];
        this.firstBlockPages = [];
        this.setCurrentPage(1);

        this.populatePagesArray();
    }

    /**
     * creates pages blocks and pages depends of total page count.
     */
    private populatePagesArray(): void {
        if (!this.totalPageCount) {
            return;
        }

        for (let i = 1; i <= this.totalPageCount; i++) {
            this.pagesArray.push(new Page(i, this.onPageClick));
        }

        if (this.isPagesTotalOffBlocks()) {
            this.firstBlockPages = this.pagesArray.slice();
            this.middleBlockPages = [];
            this.lastBlockPages = [];

            return;
        }

        this.reorganizePageBlocks();
    }

    /**
     * reorganizePageBlocks changes pages blocks organization depends of
     * current selected page index.
     */
    private reorganizePageBlocks(): void {
        if (this.isPagesTotalOffBlocks()) {
            return;
        }

        if (this.isCurrentInFirstBlock()) {
            this.setBlocksIfCurrentInFirstBlock();

            return;
        }

        if (!this.isCurrentInFirstBlock() && !this.isCurrentInLastBlock()) {
            this.setBlocksIfCurrentInMiddleBlock();

            return;
        }

        if (this.isCurrentInLastBlock()) {
            this.setBlocksIfCurrentInLastBlock();
        }
    }

    private setBlocksIfCurrentInFirstBlock(): void {
        this.firstBlockPages = this.pagesArray.slice(0, 3);
        this.middleBlockPages = [];
        this.lastBlockPages = this.pagesArray.slice(-1);
    }

    private setBlocksIfCurrentInMiddleBlock(): void {
        this.firstBlockPages = this.pagesArray.slice(0, 1);
        this.middleBlockPages = this.pagesArray.slice(this.currentPageNumber - 2, this.currentPageNumber + 1);
        this.lastBlockPages = this.pagesArray.slice(-1);
    }

    private setBlocksIfCurrentInLastBlock(): void {
        this.firstBlockPages = this.pagesArray.slice(0, 1);
        this.middleBlockPages = [];
        this.lastBlockPages = this.pagesArray.slice(-3);
    }

    private isCurrentInFirstBlock(): boolean {
        return this.currentPageNumber < this.MAX_PAGES_PER_BLOCK;
    }

    private isCurrentInLastBlock(): boolean {
        return this.totalPageCount - this.currentPageNumber < this.MAX_PAGES_PER_BLOCK - 1;
    }

    private isPagesTotalOffBlocks(): boolean {
        return this.totalPageCount <= this.MAX_PAGES_OFF_BLOCKS;
    }

    private incrementCurrentPage(): void {
        this.currentPageNumber++;
    }

    private decrementCurrentPage(): void {
        this.currentPageNumber--;
    }

    private setCurrentPage(pageNumber: number): void {
        this.currentPageNumber = pageNumber;
    }
}
</script>

<style scoped lang="scss">
    .pagination-container {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding-left: 25px;
        margin-top: 25px;

        &__pages {
            display: flex;
            align-items: center;
        }

        &__button {
            display: flex;
            align-items: center;
            justify-content: center;
            cursor: pointer;
            border: 1px solid #afb7c1;
            border-radius: 6px;
            width: 30px;
            height: 30px;

            &:hover {

                .pagination-svg-path {
                    fill: #fff !important;
                }
            }
        }

        &__items {
            margin: 0 20px;
            display: flex;

            .pages-divider {
                margin: 0 20px;
            }
        }
    }
</style>
