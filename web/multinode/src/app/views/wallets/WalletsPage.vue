// Copyright (C) 2021 Storj Labs, Inc.
// See LICENSE for copying information.

<template>
    <div class="wallets">
        <h1 class="wallets__title">Wallets</h1>
        <div class="wallets__content-area">
            <div class="wallets__left-area">
                <wallets-table
                    v-if="operatorsState.operators.length"
                    class="wallets__left-area__table"
                    :operators="operatorsState.operators"
                />
            </div>
            <div class="wallets__right-area">
                <info-block class="information">
                    <div slot="body" class="wallets__information">
                        <h3 class="wallets__information__title">Payouts with zkSync</h3>
                        <p class="wallets__information__description">Short description how minimal threshold system works.</p>
                        <v-link uri="https://forum.storj.io/t/minimum-threshold-for-storage-node-operator-payouts/11064" label="Learn more" />
                    </div>
                </info-block>
            </div>
        </div>
        <div v-if="operatorsState.pageCount > 1" class="wallets__pagination">
            <v-pagination
                :total-page-count="operatorsState.pageCount"
                :preselected-current-page-number="operatorsState.currentPage"
                :on-page-click-callback="listPaginated"
            />
            <p class="wallets__pagination__info">Showing <strong>{{ operatorsState.operators.length }} of {{ operatorsState.totalCount }}</strong> wallets</p>
        </div>
    </div>
</template>

<script lang="ts">
import { Component, Vue } from 'vue-property-decorator';

import InfoBlock from '@/app/components/common/InfoBlock.vue';
import VLink from '@/app/components/common/VLink.vue';
import VPagination from '@/app/components/common/VPagination.vue';
import WalletsTable from '@/app/components/wallets/tables/walletsSummary/WalletsTable.vue';

import { UnauthorizedError } from '@/api';
import { OperatorsState } from '@/app/store/operators';

@Component({
    components: {
        VPagination,
        VLink,
        InfoBlock,
        WalletsTable,
    },
})
export default class WalletsPage extends Vue {
    public async mounted(): Promise<void> {
        await this.listPaginated(this.operatorsState.currentPage);
    }

    /**
     * retrieves all operator related data.
     */
    public get operatorsState(): OperatorsState {
        return this.$store.state.operators;
    }

    public async listPaginated(pageNumber: number): Promise<void> {
        try {
            await this.$store.dispatch('operators/listPaginated', pageNumber);
        } catch (error) {
            if (error instanceof UnauthorizedError) {
                // TODO: redirect to login screen.
            }

            // TODO: notify error
        }
    }
}
</script>

<style lang="scss" scoped>
    .wallets {
        box-sizing: border-box;
        padding: 60px;
        overflow-y: auto;
        height: calc(100vh - 60px);

        &__title {
            font-family: 'font_bold', sans-serif;
            font-size: 32px;
            color: var(--c-title);
            margin-bottom: 36px;
        }

        &__content-area {
            display: flex;
            align-items: flex-start;
            justify-content: space-between;
            width: 100%;
            min-height: 80%;
        }

        &__left-area {
            width: 75%;
            margin-right: 24px;
        }

        &__right-area {
            width: 25%;
        }

        &__information {
            font-size: 14px;
            color: var(--c-title);

            &__title {
                font-family: 'font_bold', sans-serif;
                font-size: 16px;
                margin-bottom: 8px;
            }

            &__description {
                font-family: 'font_regular', sans-serif;
                margin-bottom: 16px;
            }

            &__link {
                text-decoration: none;
                color: var(--c-primary);
            }
        }

        &__pagination {
            width: 100%;
            display: flex;
            align-items: flex-end;
            justify-content: space-between;

            &__info {
                font-family: 'font_semiBold', sans-serif;
                font-size: 16px;
                color: #74777e;

                strong {
                    color: var(--c-title);
                }
            }
        }
    }

    .info-block {
        padding: 20px;

        &.information {
            background: #f8f8f9;
        }
    }
</style>
