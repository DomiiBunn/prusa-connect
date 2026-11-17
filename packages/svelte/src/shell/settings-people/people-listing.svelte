<!-- Everyone who can sign in, in whichever of the two shapes fits. -->
<script lang="ts">
  import KeyRoundIcon from '@lucide/svelte/icons/key-round';
  import ShieldCheckIcon from '@lucide/svelte/icons/shield-check';
  import Trash2Icon from '@lucide/svelte/icons/trash-2';

  import { formatDateTime, t } from '../../i18n';
  import {
    ABOVE_ROW_LINK,
    Badge,
    DataRow,
    type DataRowValue,
    DropdownMenuItem,
    LINKED_ROW,
    ROW_LINK,
    RowActions,
    RowList,
    Select,
    SelectContent,
    SelectItem,
    SelectTrigger,
    SelectValue,
    Table,
    TableBody,
    TableCaption,
    TableCell,
    TableHead,
    TableHeader,
    TableRow,
  } from '../../primitives';
  import { roleLabelKey } from './presentation';
  import type { PeopleFieldId, PersonRow } from './types';

  let {
    rows,
    roles,
    view,
    busy,
    shows,
    onRoleChange,
    onPassword,
    onRemove,
    personHref,
  }: {
    rows: readonly PersonRow[];
    roles: readonly string[];
    /** `table` or `rows`, as the toolbar's view switch holds it. */
    view: string;
    /** A write is in flight, so every control that starts another one waits. */
    busy: boolean;
    shows: (field: PeopleFieldId) => boolean;
    onRoleChange: (row: PersonRow, role: string) => void;
    onPassword: (row: PersonRow) => void;
    onRemove: (row: PersonRow) => void;
    /** Where one person's page is. Absent, a row opens nothing. */
    personHref?: ((id: string) => string) | undefined;
  } = $props();

  const options = $derived(
    roles.map((role) => ({ value: role, label: t(roleLabelKey(role)) })),
  );

  /** Role by colour as well as by word, as the locked badge shows it. */
  const roleTone = (row: PersonRow): 'info' | 'default' =>
    row.role === 'admin' ? 'info' : 'default';

  /** Both verdicts lock the same two controls, so both are asked once. */
  const locked = (row: PersonRow): boolean => row.isSelf || row.isLastAdmin;

  const secondaries = (row: PersonRow): DataRowValue[] => [
    row.isSelf && t('settingsPeople.you'),
    row.isLastAdmin && t('settingsPeople.onlyAdmin'),
    shows('name') && row.displayName !== row.username
      ? row.displayName
      : undefined,
    shows('added')
      ? t('settingsPeople.addedOn', { when: formatDateTime(row.createdAt) })
      : undefined,
  ];
</script>

{#snippet roleControl(row: PersonRow)}
  {#if locked(row)}
    <!-- A role that cannot change is a reading and not a control. -->
    <Badge variant={row.role === 'admin' ? 'info' : 'outline'}>
      {t(roleLabelKey(row.role))}
    </Badge>
  {:else}
    <Select
      value={row.role}
      items={options}
      disabled={busy}
      onValueChange={(next: string) => {
        onRoleChange(row, next);
      }}
    >
      <SelectTrigger
        size="sm"
        tone={roleTone(row)}
        aria-label={t('settingsPeople.role.changeFor', { name: row.username })}
      >
        <!-- An account with no role holds none of the options. -->
        <SelectValue placeholder={t('settingsPeople.role.none')} />
      </SelectTrigger>
      <SelectContent>
        {#each options as option (option.value)}
          <SelectItem value={option.value} label={option.label}>
            {option.label}
          </SelectItem>
        {/each}
      </SelectContent>
    </Select>
  {/if}
{/snippet}

{#snippet menu(row: PersonRow)}
  <RowActions name={row.username} class={ABOVE_ROW_LINK}>
    {#if personHref !== undefined}
      <DropdownMenuItem>
        {#snippet child({ props })}
          <a {...props} href={personHref(row.id)}>
            <ShieldCheckIcon aria-hidden="true" />
            {t('settingsPeople.access')}
          </a>
        {/snippet}
      </DropdownMenuItem>
    {/if}
    <DropdownMenuItem
      disabled={busy}
      onSelect={() => {
        onPassword(row);
      }}
    >
      <KeyRoundIcon aria-hidden="true" />
      {t('settingsPeople.password')}
    </DropdownMenuItem>
    <!-- Kept and disabled rather than dropped. -->
    <DropdownMenuItem
      variant="destructive"
      disabled={busy || locked(row)}
      onSelect={() => {
        onRemove(row);
      }}
    >
      <Trash2Icon aria-hidden="true" />
      {t('settingsPeople.remove')}
    </DropdownMenuItem>
  </RowActions>
{/snippet}

{#snippet marks(row: PersonRow)}
  {#if row.isSelf}
    <Badge variant="outline">{t('settingsPeople.you')}</Badge>
  {/if}
  {#if row.isLastAdmin}
    <!-- Outline, not `warning`: nothing is wrong with this account. -->
    <Badge variant="outline">{t('settingsPeople.onlyAdmin')}</Badge>
  {/if}
{/snippet}

{#snippet table()}
  <Table>
    <TableCaption class="sr-only">{t('settingsPeople.caption')}</TableCaption>
    <TableHeader>
      <TableRow>
        <TableHead>{t('settingsPeople.username')}</TableHead>
        {#if shows('name')}<TableHead>{t('settingsPeople.name')}</TableHead
          >{/if}
        {#if shows('role')}<TableHead>{t('settingsPeople.role')}</TableHead
          >{/if}
        {#if shows('added')}<TableHead>{t('settingsPeople.added')}</TableHead
          >{/if}
        <TableHead class="sr-only">{t('settingsPeople.actions')}</TableHead>
      </TableRow>
    </TableHeader>
    <TableBody>
      {#each rows as row (row.id)}
        <TableRow class={personHref === undefined ? undefined : LINKED_ROW}>
          <TableCell>
            <span class="flex flex-wrap items-center gap-2">
              {#if personHref === undefined}
                <span class="font-mono">{row.username}</span>
              {:else}
                <a
                  href={personHref(row.id)}
                  class={[
                    'font-mono outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background',
                    ROW_LINK,
                  ]}>{row.username}</a
                >
              {/if}
              {@render marks(row)}
            </span>
          </TableCell>
          {#if shows('name')}<TableCell>{row.displayName}</TableCell>{/if}
          {#if shows('role')}
            <TableCell>
              <span class={['inline-flex', ABOVE_ROW_LINK]}>
                {@render roleControl(row)}
              </span>
            </TableCell>
          {/if}
          {#if shows('added')}
            <TableCell class="text-xs tabular-nums">
              {formatDateTime(row.createdAt)}
            </TableCell>
          {/if}
          <TableCell class="text-right">{@render menu(row)}</TableCell>
        </TableRow>
      {/each}
    </TableBody>
  </Table>
{/snippet}

{#snippet stacked()}
  <RowList>
    {#each rows as row (row.id)}
      <DataRow
        primary={row.username}
        {...personHref === undefined ? {} : { href: personHref(row.id) }}
        mono
        wrapValues
        secondaryValues={secondaries(row)}
      >
        {#snippet actions()}
          <div class="flex items-center gap-2">
            {#if shows('role')}{@render roleControl(row)}{/if}
            {@render menu(row)}
          </div>
        {/snippet}
      </DataRow>
    {/each}
  </RowList>
{/snippet}

<div class="@container/people">
  {#if view === 'table'}
    <div class="hidden @[44rem]/people:block">{@render table()}</div>
    <div class="@[44rem]/people:hidden">{@render stacked()}</div>
  {:else}
    {@render stacked()}
  {/if}
</div>
