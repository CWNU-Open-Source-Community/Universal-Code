import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../../api/client";
import type { MCPInput } from "../../api/mcp-inputs";
import { MCPInputCards } from "./mcp-input-cards";
function renderInputs(overrides: Partial<MCPInput> = {}) {
 const input: MCPInput = { id:"input-1",run_id:"run-1",server_id:"server-1",server_name:"Reviewed server",method:"tools/call",message:"Choose a project",mode:"form",expires_at:new Date(Date.now()+60000).toISOString(),fields:[{name:"project",title:"Project",type:"string",required:true,enum:["blue","green"],enum_titles:["Blue","Green"],default:"blue"}],...overrides };
 const get=vi.fn().mockResolvedValue({version:"mcp-input.v1",items:[input]});
 const respondMCPInput=vi.fn().mockImplementation(async()=>{get.mockResolvedValue({version:"mcp-input.v1",items:[]});return {accepted:true}});
 const client={get,respondMCPInput,hasControl:true,hasExtensionControl:true} as unknown as APIClient;
 const queryClient=new QueryClient({defaultOptions:{queries:{retry:false}}});
 render(<QueryClientProvider client={queryClient}><MCPInputCards client={client} runID="run-1" /></QueryClientProvider>);
 return {get,respondMCPInput,queryClient};
}
it("shows default selection and sends a single explicit answer",async()=>{
 const {respondMCPInput,queryClient}=renderInputs();
 const select=await screen.findByRole("combobox");expect(select).toHaveValue("blue");
 await userEvent.selectOptions(select,"green");await userEvent.click(screen.getByRole("button",{name:"提交答复"}));
 await waitFor(()=>expect(respondMCPInput).toHaveBeenCalledOnce());
 expect(respondMCPInput).toHaveBeenCalledWith("run-1","input-1",{version:"mcp-input.v1",action:"accept",content:{project:"green"}});
 expect(queryClient.getMutationCache().getAll()).toHaveLength(0);
 await waitFor(()=>expect(screen.queryByRole("combobox")).not.toBeInTheDocument());
});
it("shows external host and manual link without visiting or answering automatically",async()=>{
 const {respondMCPInput,get}=renderInputs({mode:"url",fields:null,url:"https://auth.example/authorize?state=private",host:"auth.example"});
 const link=await screen.findByRole("link",{name:"打开外部授权页面"});
 expect(link).toHaveAttribute("href","https://auth.example/authorize?state=private");expect(link).toHaveAttribute("rel","noopener noreferrer");expect(screen.getByText("auth.example")).toBeInTheDocument();
 expect(respondMCPInput).not.toHaveBeenCalled();expect(get).toHaveBeenCalledWith("/runs/run-1/mcp-inputs",{},expect.any(AbortSignal));
 await userEvent.click(screen.getByRole("button",{name:"已完成"}));
 expect(respondMCPInput).toHaveBeenCalledWith("run-1","input-1",{version:"mcp-input.v1",action:"accept"});
});
it.each(["拒绝提供","取消交互"])("%s sends no form content",async(label)=>{
 const {respondMCPInput}=renderInputs();await userEvent.click(await screen.findByRole("button",{name:label}));
 expect(respondMCPInput).toHaveBeenCalledWith("run-1","input-1",{version:"mcp-input.v1",action:label==="拒绝提供"?"decline":"cancel"});
});
it("does not silently round an integer answer", async () => {
 const {respondMCPInput}=renderInputs({fields:[{name:"amount",title:"Amount",type:"integer",required:true}]});
 await userEvent.type(await screen.findByRole("spinbutton"),"9007199254740993");
 await userEvent.click(screen.getByRole("button",{name:"提交答复"}));
 expect(await screen.findByRole("alert")).toHaveTextContent("精确表示");expect(respondMCPInput).not.toHaveBeenCalled();
 await userEvent.click(screen.getByRole("button",{name:"拒绝提供"}));
 expect(respondMCPInput).toHaveBeenCalledWith("run-1","input-1",{version:"mcp-input.v1",action:"decline"});
});

it("does not reuse another API client request or answer", async () => {
 const first = {id:"same-id",run_id:"run-1",server_id:"server-1",server_name:"First host",method:"tools/call",message:"Private first question",mode:"form",expires_at:new Date(Date.now()+60000).toISOString(),fields:[]} satisfies MCPInput;
 const oldClient={get:vi.fn().mockResolvedValue({version:"mcp-input.v1",items:[first]}),respondMCPInput:vi.fn(),hasControl:true,hasExtensionControl:true} as unknown as APIClient;
 const newClient={get:vi.fn().mockReturnValue(new Promise(()=>{})),respondMCPInput:vi.fn(),hasControl:true,hasExtensionControl:true} as unknown as APIClient;
 const queries=new QueryClient({defaultOptions:{queries:{retry:false,staleTime:60000}}});
 const {rerender}=render(<QueryClientProvider client={queries}><MCPInputCards client={oldClient} runID="run-1" /></QueryClientProvider>);
 await screen.findByText("Private first question");
 rerender(<QueryClientProvider client={queries}><MCPInputCards client={newClient} runID="run-1" /></QueryClientProvider>);
 expect(screen.queryByText("Private first question")).not.toBeInTheDocument();
 expect(screen.queryByRole("button",{name:"提交答复"})).not.toBeInTheDocument();
 expect(newClient.respondMCPInput).not.toHaveBeenCalled();
});
it("requires extension control even when run control is available", async () => {
 const input={id:"input",run_id:"run-1",server_id:"server-1",server_name:"Server",method:"tools/call",message:"Question",mode:"url",url:"https://auth.example/",host:"auth.example",expires_at:new Date(Date.now()+60000).toISOString(),fields:null} satisfies MCPInput;
 const client={get:vi.fn().mockResolvedValue({version:"mcp-input.v1",items:[input]}),respondMCPInput:vi.fn(),hasControl:true,hasExtensionControl:false} as unknown as APIClient;
 render(<QueryClientProvider client={new QueryClient()}><MCPInputCards client={client} runID="run-1" /></QueryClientProvider>);
 expect(await screen.findByRole("button",{name:"已完成"})).toBeDisabled();
 expect(screen.getByRole("button",{name:"拒绝提供"})).toBeDisabled();
 expect(screen.getByRole("button",{name:"取消交互"})).toBeDisabled();
});
