# تغییر نام ها
برای اینکه ابهام کمتر بشه

اسم User را به Client تغییر بده

به جای Manager هم بذار Reseller

اینجوری ابهام بین مدیر سیستم و کاربران سیستم و کلاینت و ریسلر پیش نمیاد

به جای Agent هم کلمه Agent استفاده میکنیم

# کاربران سیستم
### Client
با استفاده از user pass که در authmethod تعریف شده بتونه لاگین کنه و توکن jwt بگیره
AuthAgent{
   login(user,pass)->jwt_token // when 2fa auth is not enabled for user
   login(passkey)->jwt_token
   logout
   change_password
   refresh
}

در صورت اشتباه وارد شدن user توسط یک IP چند دفعه (قابل تعریف در تنظیمات) به مدت چند دقیقه بلاک بشه و در ایونت نیز ثبت بشه
### Reseller
با استفاده از همون AuthAgent ولی با استفاده از یوزر و پسورد هش شده ریسلر انجام بشه
ما در کلاینت به علت اینکه در سرویس های مختلف میخواهیم استفاده کنیم هش نمیکنیم ولی برای ریسلر باید هش بشه

### Owner
عملا همه کاره سیستم هست و فقط با APIKey وصل میشه هر کار ادمین در ایونت ثبت میشه
ادمین میتونه متود login(user,"anypass") اجرا کنه
و توکن jwt بگیره
همینطور ادمین انگار پدر تمام Reseller ها هم هست بنابراین تمام api های مربوط به ریسلر ها هم دسترسی داره


### Agent
a Agent is known by API key. Each Agent should have their own API KEY and will authenticate with their API KEY


Therefore in the headers we have either HUE_API_KEY for Agent and Owner or authorization Header for Reseller and Clients



# جدا سازی api ادمین و ریسلر

در قسمت [AdminService](https://github.com/hiddify/HUE/blob/88651c996825180174774a02a4cd0cb4120fb6e8/api/proto/hue/v1/hue.proto#L680)
تبدیل بشه به دو بخش
```proto
service ResellerClientService{
  // Clients
  rpc CreateClient(CreateClientRequest) returns (Client) {
    option (google.api.http) = {
      post: "/v1/Clients"
      body: "Client"
    };
  }
  rpc GetClient(GetClientRequest) returns (Client) {
    option (google.api.http) = {get: "/v1/Clients/{id}"};
  }
  rpc ListClients(ListClientsRequest) returns (ListClientsResponse) {
    option (google.api.http) = {get: "/v1/Clients"};
  }
  rpc UpdateClient(UpdateClientRequest) returns (Client) {
    option (google.api.http) = {
      patch: "/v1/Clients/{id}"
      body: "Client"
    };
  }
  rpc DeleteClient(DeleteClientRequest) returns (google.protobuf.Empty) {
    option (google.api.http) = {delete: "/v1/Clients/{id}"};
  }
  rpc ResetClientUsage(ResetClientUsageRequest) returns (UsagePlan) {
    option (google.api.http) = {
      post: "/v1/Clients/{Client_id}:resetUsage"
      body: "*"
    };
  }

  // Usage plans
  rpc GetUsagePlan(GetUsagePlanRequest) returns (UsagePlan) {
    option (google.api.http) = {get: "/v1/usagePlans/{id}"};
  }
  rpc ListUsagePlans(ListUsagePlansRequest) returns (ListUsagePlansResponse) {
    option (google.api.http) = {get: "/v1/usagePlans"};
  }
  rpc GetActiveUsagePlan(GetActiveUsagePlanRequest) returns (UsagePlan) {
    option (google.api.http) = {get: "/v1/Clients/{Client_id}/activeUsagePlan"};
  }

   rpc GetAvailbleNodesInfo(Empty) returns (NodeInfo) {
}
```

```proto
  service ResellerManagementService{
  // Resellers
  rpc CreateReseller(CreateResellerRequest) returns (Reseller) {//it can only creates sub resellers or recursive sub sub reseller
    option (google.api.http) = {
      post: "/v1/Resellers"
      body: "Reseller"
    };
  }
  rpc GetReseller(GetResellerRequest) returns (Reseller) { //it can only get sub resellers or recursive sub sub reseller
    option (google.api.http) = {get: "/v1/Resellers/{id}"};
  }
  rpc ListResellers(ListResellersRequest) returns (ListResellersResponse) {//it can only get sub resellers or recursive sub sub reseller
    option (google.api.http) = {get: "/v1/Resellers"};
  }
  rpc UpdateReseller(UpdateResellerRequest) returns (Reseller) {//it can only update sub resellers or recursive sub sub reseller
    option (google.api.http) = {
      patch: "/v1/Resellers/{id}"
      body: "Reseller"
    };
  }
  rpc DeleteReseller(DeleteResellerRequest) returns (google.protobuf.Empty) { //it can only delete sub resellers or recursive sub sub reseller
    option (google.api.http) = {delete: "/v1/Resellers/{id}"};
  }
  rpc ResetResellerUsage(ResetResellerUsageRequest) returns (Reseller) { //it can only reset sub resellers or recursive sub sub reseller
    option (google.api.http) = {
      post: "/v1/Resellers/{Reseller_id}:resetUsage"
      body: "*"
    };
  }
 
}
message NodeInfo{
id
name
geo
}
‍‍‍```

```proto
service AdminService{
rpc CreateNode(CreateNodeRequest) returns (Node) { // a node will also create automatically if a Agent calling via a valid auth key not listed IP
    option (google.api.http) = {
      post: "/v1/nodes"
      body: "node"
    };
  }
  rpc GetNode(GetNodeRequest) returns (Node) {
    option (google.api.http) = {get: "/v1/nodes/{id}"};
  }
  rpc ListNodes(ListNodesRequest) returns (ListNodesResponse) {
    option (google.api.http) = {get: "/v1/nodes"};
  }
  rpc UpdateNode(UpdateNodeRequest) returns (Node) {
    option (google.api.http) = {
      patch: "/v1/nodes/{id}"
      body: "node"
    };
  }
  rpc DeleteNode(DeleteNodeRequest) returns (google.protobuf.Empty) {
    option (google.api.http) = {delete: "/v1/nodes/{id}"};
  }
  rpc ResetNodeUsage(ResetNodeUsageRequest) returns (Node) {
    option (google.api.http) = {
      post: "/v1/nodes/{node_id}:resetUsage"
      body: "*"
    };
  }
// Agents
  rpc CreateAgent(CreateAgentRequest) returns (CreateAgentResponse) {
    option (google.api.http) = {
      post: "/v1/Agents"
      body: "Agent"
    };
  }
  rpc GetAgent(GetAgentRequest) returns (Agent) {
    option (google.api.http) = {get: "/v1/Agents/{id}"};
  }
  rpc ListAgents(ListAgentsRequest) returns (ListAgentsResponse) {
    option (google.api.http) = {get: "/v1/Agents"};
  }
  rpc UpdateAgent(UpdateAgentRequest) returns (Agent) {
    option (google.api.http) = {
      patch: "/v1/Agents/{id}"
      body: "Agent"
    };
  }
  rpc DeleteAgent(DeleteAgentRequest) returns (google.protobuf.Empty) {
    option (google.api.http) = {delete: "/v1/Agents/{id}"};
  }

// Events
  rpc ListEvents(ListEventsRequest) returns (ListEventsResponse) {
    option (google.api.http) = {get: "/v1/events"};
  }
  rpc StreamEvents(StreamEventsRequest) returns (stream Event) {
    option (google.api.http) = {get: "/v1/events:stream"};
  }
}
```


```proto
message CreateApiKeyRequest {//بیشتر باید کار بشه روش
  ApiKey api_key = 1 [(google.api.field_behavior) = REQUIRED];
  Type agent/owner/jwt
  agent_id #if is agent
  expire time
}
service AuthAdminService{
// API keys
  rpc CreateApiKey(CreateApiKeyRequest) returns (CreateApiKeyResponse) {
    option (google.api.http) = {
      post: "/v1/apiKeys"
      body: "api_key"
    };
  }
  rpc ListApiKeys(ListApiKeysRequest) returns (ListApiKeysResponse) {
    option (google.api.http) = {get: "/v1/apiKeys"};
  }
  rpc RevokeApiKey(RevokeApiKeyRequest) returns (ApiKey) {
    option (google.api.http) = {
      post: "/v1/apiKeys/{id}:revoke"
      body: "*"
    };
  }
}
```

#افزودن کانفیگ به نود 
یه مشکلی که hue داره اینه که همه ی نود های باید کانفیگ های یکسان داشته باشند که جالب نیست
هر نود باید کانفیگ های خودشو داشته باشه
عملا یعنی توی دیتابیسش ذخیره بشه

در نتیجه برای Node ما این اطلاعات را عملا خواهیم داشت

```proto
message Node {
  ...
  map<string, google.protobuf.Value> config
}
```



برای اینکه شفاف بشه

Node
یه شی هست که صرفا مجازی خواهد بود

یعنی روی یه نود مثلا یه سرور هتزنر میتونه چندتا سرویس باشه

وقتی میگیم پهنای باند کلی نود مثلا ۲ ترابایت هست
جمع همه سرویس ها نباید بیشتر از ۲ ترابایت بشه

بنابراین در حال حاضر هیچ سرویسی برای نود نداریم https://github.com/hiddify/HUE/blob/88651c996825180174774a02a4cd0cb4120fb6e8/api/proto/hue/v1/hue.proto#L853

# نحوه همگام سازی کانفیگ ها
هر سرویس با توجه به IP که متصل میشه. سیستم متوجه میشه به کدوم نود تعلق داره.

در نتیجه موقع سینک کانفیگ میاد و کانفیگ مربوط به نود را ارایه میکنه

بنابراین برای سینک کانفیگ ها
```
service ConfigService{
 rpc sync_config()-> 
 
}

message SyncConfigResponse {
  map<string, google.protobuf.Value> config // keys should be sth like xray.numeric_version., wiregaurd.numeric_version. and value can be full config numeric_version is int that is related to config for a specific version greater than the numeric version
   
  // Stable identifier for this snapshot. Pass back as current_etag on
  // the next call to short-circuit when unchanged.
  string etag = 4;
  bool changed = 5;
  google.protobuf.Timestamp updated_at = 6;
}
```

# Domain Certificate Service

از اونجایی که دامنه یه آیتم جنرال هست و میتونه در جاهای مختلفی استفاده بشه که باید بین سیستم ها به اشتراک گذاشته بشه

هر سرویس میتونه یه سرتیفیکیت اضافه کنه 
اگر از قبل سرتیفیکیتی وجود داشته باشه و ولید باشه فقط در صورتی اضافه میشه که سرتیفیکیت جدید هم ولید باشه وگرنه ریجکت میشه

موقع دریافت نامه دامنه باید * و اینکه یه سرتیفیکیت میتونه چندتا دامنه را داشته باشه هم لحاظ بشه
همینطور IP هم میتونه سرتیفیکیت داشته باشه
```
message DomainCerticate{
domain_names
public_key
private_key
expire_date
generated_node
valid
}
service DomainCertificateService{
   addCertificate() 
   getCertificate(domain)
   allCertificates()
}

```


# IP Restrication
ةHue بدون سرتیفیکیت هرگز نمیتونه به 0.0.0.0 لیسن کنه و حتما باید به آی پی اینولید گوش بده
اما در صورت اجرا در حالت سکیور میتونه 
همینطور در صورتی که فلگ سکیور فعال باشه و دامنه مشخص بشه hue اگر سرتیفیکی در استور نباشه یه سرتیفیکیت ۳۰ ساله میسازه برای دامنه خودش و به سرتیفیکیت ها اضافه میکنه
در صورتی که کاربر فایل سرتیفیکیت ارایه کن این سرتیفیکیت ها به استور اضافه میشه




# Agents

agents should be in a separated package

## Xray Agent
xray agent should receive the xray grpc api server or xray_file_path and add/remove inbounds/outbounds

the user_info holders like
{client:uuid} {client:username} {client:password} {domain_cert_private_key}
should be replaced with proper value



in the config
xray.version, there maybe multiple version exist. it should check xray current version and select a config version which xray.version<=current_cersion<=next_xray.version




# Tests

in the test an instance of Hue should be run

then some client/resellers with usage limit should be added

add agent 

set a xray simple vless config to the node

then an instance of xray should be launch 
and an xray_agent
and do internal speedtest
so it should be able to add/remvoe proxy and update users usage


all the constraints should be passed
