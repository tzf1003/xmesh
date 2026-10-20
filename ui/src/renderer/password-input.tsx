import type { JSX } from "preact";
import { useState } from "preact/hooks";
import { Icon } from "./icons";
import { useCopy } from "./design-copy";
export function PasswordInput({ value, onInput, testid, autocomplete = "off", invalid, maxLength }: { value: string; onInput(value:string):void; testid:string; autocomplete?:string; invalid?:boolean; maxLength?:number }): JSX.Element {
 const [visible,setVisible]=useState(false);const c=useCopy();
 return <div class="password-input"><input type={visible?"text":"password"} value={value} onInput={e=>onInput(e.currentTarget.value)} autocomplete={autocomplete} aria-invalid={invalid} maxLength={maxLength} data-testid={testid}/><button type="button" class="password-toggle" aria-pressed={visible} aria-label={visible?c("隐藏密码","Hide password"):c("显示密码","Show password")} onClick={e=>{e.preventDefault();setVisible(v=>!v);}} data-testid={`${testid}-toggle`}><Icon name={visible?"eyeOff":"eye"}/></button></div>;
}
