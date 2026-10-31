import React, { useEffect, useId, useRef } from 'react';
import { Ellipsis } from 'lucide-react';

// A ⋯ button and a native popover under it (KyYard's row menu). Escape and an outside click
// close it; scrolling closes it so it never detaches. Arrow keys move between items.
export function MoreMenu({ label, children }: { label: string; children: (close: () => void) => React.ReactNode }) {
  const menu = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const id = `more-menu-${useId().replace(/:/g, '')}`;
  const close = () => menu.current?.hidePopover?.();
  useEffect(() => {
    const el = menu.current;
    if (!el) return;
    const onScroll = () => close();
    const toggled = (event: Event) => {
      const open = (event as ToggleEvent).newState === 'open';
      trigger.current?.setAttribute('aria-expanded', String(open));
      if (!open) { window.removeEventListener('scroll', onScroll, true); return; }
      const at = trigger.current!.getBoundingClientRect();
      const below = at.bottom + 4 + el.offsetHeight <= window.innerHeight;
      el.style.top = `${below ? at.bottom + 4 : Math.max(8, at.top - 4 - el.offsetHeight)}px`;
      el.style.left = `${Math.max(8, at.right - el.offsetWidth)}px`;
      el.querySelector<HTMLElement>('.ky-menu-item:not(:disabled)')?.focus();
      window.addEventListener('scroll', onScroll, true);
    };
    el.addEventListener('toggle', toggled);
    return () => { el.removeEventListener('toggle', toggled); window.removeEventListener('scroll', onScroll, true); };
  }, []);
  return <>
    <button ref={trigger} type="button" className="ky-menu-trigger" popoverTarget={id} aria-haspopup="menu" aria-expanded="false" aria-label={label} title={label}>
      <Ellipsis size={18} aria-hidden="true" />
    </button>
    <div ref={menu} id={id} popover="auto" className="ky-menu" role="menu" aria-label={label} onKeyDown={(event) => {
      if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return;
      event.preventDefault();
      const items = [...event.currentTarget.querySelectorAll<HTMLElement>('.ky-menu-item:not(:disabled)')];
      const at = items.indexOf(document.activeElement as HTMLElement);
      items[(at + (event.key === 'ArrowDown' ? 1 : items.length - 1)) % items.length]?.focus();
    }}>{children(close)}</div>
  </>;
}
