import React from 'react';
import {flushSync} from 'react-dom';
import {createRoot, type Root} from 'react-dom/client';
import {Checkbox, Input, ListBox, SearchField, Select, TextArea} from '@heroui/react';
import {Segment} from '@heroui-pro/react/segment';
import {Icon, type IconName} from './icon';

// Form controls inside the controller's dialogs and settings: HeroUI Select, Checkbox, Input, TextArea and
// SearchField, and the Pro Segment. The controller drives them through host elements that behave like the native controls they
// replace (value, checked, disabled, change events; selectField() and the others in app.js), so these components
// only render what they are given and report what the user picks.

export interface SelectOption {value: string; label: string; disabled?: boolean}
export interface SelectFieldProps {label: string; options: SelectOption[]; value: string; disabled?: boolean; placeholder?: string; onChange(value: string): void}

// An empty string is a valid <option> value but not a React Aria key, so it travels under a sentinel key.
const EMPTY = '\u0000empty';
const toKey = (value: string) => value === '' ? EMPTY : value;
const fromKey = (key: unknown) => key == null || key === EMPTY ? '' : String(key);

export function SelectField({label, options, value, disabled, placeholder, onChange}: SelectFieldProps) {
 const known = options.some((option) => option.value === value);
 return <Select aria-label={label} className="hero-select-field" fullWidth isDisabled={disabled} placeholder={placeholder || ' '}
  value={known ? toKey(value) : null} disabledKeys={options.filter((option) => option.disabled).map((option) => toKey(option.value))}
  onChange={(key) => onChange(fromKey(key))}>
  <Select.Trigger className="hero-select-trigger">
   <Select.Value/>
   <Select.Indicator/>
  </Select.Trigger>
  <Select.Popover className="hero-select-popover">
   <ListBox aria-label={label}>
    {options.map((option) => <ListBox.Item key={toKey(option.value)} id={toKey(option.value)} textValue={option.label}>
     {option.label}
     <ListBox.ItemIndicator/>
    </ListBox.Item>)}
   </ListBox>
  </Select.Popover>
 </Select>;
}

export interface CheckFieldProps {label: string; text?: string; checked: boolean; disabled?: boolean; onChange(checked: boolean): void}

// Without text the checkbox is the control alone, named by label; the row around it carries the visible text.
export function CheckField({label, text, checked, disabled, onChange}: CheckFieldProps) {
 return <Checkbox aria-label={text ? undefined : label} className="hero-check-field" isSelected={checked} isDisabled={disabled} onChange={onChange}>
  <Checkbox.Content className="hero-check-content">
   <Checkbox.Control><Checkbox.Indicator/></Checkbox.Control>
   {text ? <span className="hero-check-text">{text}</span> : null}
  </Checkbox.Content>
 </Checkbox>;
}

export interface SegmentItem {id: string; label: string; icon?: IconName}
export interface SegmentFieldProps {label: string; items: SegmentItem[]; selected: string; disabled?: boolean; size?: 'sm' | 'md' | 'lg'; onChange(id: string): void}

export function SegmentField({label, items, selected, disabled, size, onChange}: SegmentFieldProps) {
 return <Segment aria-label={label} className="hero-segment" size={size} selectedKey={selected} isDisabled={disabled} onSelectionChange={(key) => onChange(String(key))}>
  {items.map((item) => <Segment.Item key={item.id} id={item.id} className="hero-segment-item">
   {item.icon ? <Icon name={item.icon} size="xs"/> : null}
   {item.label}
  </Segment.Item>)}
 </Segment>;
}

// Text fields keep the labels the controller draws around them, so they are HeroUI's standalone Input and TextArea
// (TextField adds a label, description and error slot these forms do not use). They are uncontrolled, as native
// fields are: the element holds the value and the host reads and writes it there. A controlled React input also
// copies every keystroke into the value attribute, which would put a typed password into the page's HTML.
export interface TextBoxProps {
 label?: string; multiline?: boolean; disabled?: boolean; readOnly?: boolean; value: string;
 // Native attributes for the <input> or <textarea> (placeholder, maxLength, type, required, pattern, rows and so on);
 // constraint validation stays the browser's, so form.reportValidity() works as before.
 input: Record<string, unknown>;
 inputRef(element: HTMLInputElement | HTMLTextAreaElement | null): void;
}

export function TextBox({label, multiline, disabled, readOnly, value, input, inputRef}: TextBoxProps) {
 const [initial] = React.useState(value);
 const props = {...input, 'aria-label': label || undefined, defaultValue: initial, disabled, readOnly, fullWidth: true};
 return multiline ? <TextArea ref={inputRef as React.Ref<HTMLTextAreaElement>} {...props}/> : <Input ref={inputRef as React.Ref<HTMLInputElement>} {...props}/>;
}

// SearchField owns its value (the clear button and Escape empty it), so here the value is controlled. The
// controller's value is applied when revision changes; between those the field keeps what the user types.
export interface SearchBoxProps {
 label?: string; disabled?: boolean; value: string; revision: number; input: Record<string, unknown>;
 inputRef(element: HTMLInputElement | null): void; onChange(value: string): void; onClear(): void;
}

export function SearchBox({label, disabled, value, revision, input, inputRef, onChange, onClear}: SearchBoxProps) {
 const [text, setText] = React.useState(value);
 const [seen, setSeen] = React.useState(revision);
 if (seen !== revision) {setSeen(revision); setText(value);}
 // The state changes inside the input event, so React never restores a stale value under the caret or an IME.
 return <SearchField aria-label={label || undefined} className="hero-search-field" fullWidth isDisabled={disabled} value={text} validationBehavior="aria"
  onChange={(next) => {setText(next); onChange(next);}} onClear={onClear}>
  <SearchField.Group>
   <SearchField.SearchIcon/>
   <SearchField.Input ref={inputRef} {...input}/>
   <SearchField.ClearButton/>
  </SearchField.Group>
 </SearchField>;
}

const FIELDS = {select: SelectField, check: CheckField, segment: SegmentField, text: TextBox, search: SearchBox} as const;
export type FieldKind = keyof typeof FIELDS;

// Mounts and updates are applied in a microtask, in one flushSync for all pending fields. Controller code often
// builds controls while a React island is committing (settings and drawer panels are drawn from layout effects),
// where React cannot render synchronously; the microtask runs once that commit is done and before the next paint.
const pending = new Map<Root, React.ReactElement | null>();
let queued = false;
function schedule(root: Root, element: React.ReactElement | null) {
 pending.set(root, element);
 if (queued) return;
 queued = true;
 queueMicrotask(() => {
  queued = false;
  const batch = [...pending];
  pending.clear();
  flushSync(() => {for (const [target, next] of batch) if (next) target.render(next);});
  for (const [target, next] of batch) if (!next) target.unmount();
 });
}

export function mountField(container: HTMLElement, kind: FieldKind, initial: object) {
 const root = createRoot(container);
 const Component = FIELDS[kind] as React.ComponentType<any>;
 let disposed = false;
 const update = (props: object) => {if (!disposed) schedule(root, <Component {...props}/>);};
 update(initial);
 return {update, unmount: () => {if (!disposed) {disposed = true; schedule(root, null);}}};
}
