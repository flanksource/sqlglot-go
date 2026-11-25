// @live
import React from 'react';
import { Arrow, BoxNode, COLORS, Diagram } from '@flanksource/facet';

export default function ReviewedLifecycle() {
  return (
    <div id="migration-diagram" className="w-[46.5rem] p-5" style={{ backgroundColor: COLORS.background }}>
      <Diagram className="relative">
        {(id) => (
          <>
            <div className="grid grid-cols-4 items-stretch gap-8">
              <BoxNode id={id('inspect')} title="Inspect" headerColor={COLORS.accent}>
                Read the target
              </BoxNode>
              <BoxNode id={id('plan')} title="Plan" headerColor={COLORS.accent}>
                Write the plan file
              </BoxNode>
              <BoxNode id={id('review')} title="Review" headerColor={COLORS.accent}>
                Check SQL or row changes
              </BoxNode>
              <BoxNode id={id('apply')} title="Apply" headerColor={COLORS.accent} borderColor={COLORS.outputBorder}>
                Recheck and change the target
              </BoxNode>
            </div>
            <Arrow from={id('inspect')} to={id('plan')} startAnchor="right" endAnchor="left" path="straight" dashness={false} />
            <Arrow from={id('plan')} to={id('review')} startAnchor="right" endAnchor="left" path="straight" dashness={false} />
            <Arrow from={id('review')} to={id('apply')} startAnchor="right" endAnchor="left" path="straight" dashness={false} />
          </>
        )}
      </Diagram>
    </div>
  );
}
