// Copyright (C) 2020 Storj Labs, Inc.
// See LICENSE for copying information.

<template>
    <div class="bucket-bullet">
        <p class="bucket-bullet__label">{{ name }}</p>
        <UnselectIcon class="bucket-bullet__icon" @click.stop="toggleBucketSelection(name)" />
    </div>
</template>

<script lang="ts">
import { Component, Prop, Vue } from 'vue-property-decorator';

import UnselectIcon from '@/../static/images/accessGrants/unselect.svg';

import { ACCESS_GRANTS_ACTIONS } from '@/store/modules/accessGrants';

@Component({
    components: {
        UnselectIcon,
    },
})
export default class BucketNameBullet extends Vue {
    @Prop({default: ''})
    public readonly name: string;

    /**
     * Toggles bucket selection.
     */
    public toggleBucketSelection(name: string): void {
        this.$store.dispatch(ACCESS_GRANTS_ACTIONS.TOGGLE_BUCKET_SELECTION, name);
    }
}
</script>

<style scoped lang="scss">
    .bucket-bullet {
        padding: 5px 5px 5px 10px;
        display: flex;
        align-items: center;
        background: #e7eaee;
        border-radius: 50px;
        margin: 0 5px 5px 0;

        &__label {
            font-family: 'font_bold', sans-serif;
            font-style: normal;
            font-weight: bold;
            font-size: 9px;
            line-height: 12px;
            white-space: nowrap;
            text-overflow: ellipsis;
            overflow: hidden;
            max-width: 190px;
            letter-spacing: 0.05em;
            color: #1b2533;
            margin: 0;
        }

        &__icon {
            cursor: pointer;
            margin-left: 10px;
        }
    }
</style>
