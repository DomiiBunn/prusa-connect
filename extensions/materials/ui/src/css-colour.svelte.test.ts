import { describe, expect, it } from 'vitest';

import { shownColour } from './css-colour';

describe('shownColour', () => {
  it.each([
    ['#9bb085ff', '#9bb085'],
    ['#9BB085FF', '#9BB085'],
    ['#9bb08580', '#9bb08580'],
    ['#9bb08500', '#9bb08500'],
    ['#9bb085', '#9bb085'],
  ])('reads %s as %s', (stored, shown) => {
    expect(shownColour(stored)).toBe(shown);
  });
});
