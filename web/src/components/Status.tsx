import React from 'react';

export type Tone = 'danger' | 'warning' | 'success';

/** A state badge: ky-ui's `.ky-status` pill, a dot then the word. No tone is neutral. */
export function Status({ tone, ...rest }: { tone?: Tone } & React.HTMLAttributes<HTMLSpanElement>) {
  return <span className="ky-status" data-ky-status={tone} {...rest} />;
}
