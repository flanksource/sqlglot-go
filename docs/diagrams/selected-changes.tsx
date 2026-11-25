// @live
import React from 'react';
import { Arrow, BoxNode, COLORS, Diagram } from '@flanksource/facet';

export default function SelectedChanges() {
  return (
    <div id="migration-diagram" className="w-[46.5rem] p-5" style={{ backgroundColor: COLORS.background }}>
      <Diagram className="relative">
        {(id) => (
          <>
            <div className="grid grid-cols-4 items-center gap-8">
              <div className="grid gap-5">
                <BoxNode id={id('reference')} title="Reference" headerColor={COLORS.accent}>
                  Desired schema
                </BoxNode>
                <BoxNode id={id('current')} title="Target" headerColor={COLORS.accent}>
                  Current schema
                </BoxNode>
              </div>
              <BoxNode id={id('compare')} title="Compare" headerColor={COLORS.accent}>
                Select changes
              </BoxNode>
              <BoxNode id={id('patch')} title="Reviewed patch" headerColor={COLORS.accent}>
                Save the plan file
              </BoxNode>
              <BoxNode id={id('apply')} title="Apply to target" headerColor={COLORS.accent} borderColor={COLORS.outputBorder}>
                Run the reviewed SQL
              </BoxNode>
            </div>
            <Arrow from={id('reference')} to={id('compare')} startAnchor="right" endAnchor={{ position: 'left', offset: { y: -15 } }} path="straight" dashness={false} />
            <Arrow from={id('current')} to={id('compare')} startAnchor="right" endAnchor={{ position: 'left', offset: { y: 15 } }} path="straight" dashness={false} />
            <Arrow from={id('compare')} to={id('patch')} startAnchor="right" endAnchor="left" path="straight" dashness={false} />
            <Arrow from={id('patch')} to={id('apply')} startAnchor="right" endAnchor="left" path="straight" dashness={false} />
          </>
        )}
      </Diagram>
    </div>
  );
}
