// Copyright 2022 iLogtail Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

#include <memory>
#include <iostream>
#include "parser.h"
#include "inner_parser.h"
#include "logger/Logger.h"
#include "interface/helper.h"

namespace logtail {

ParseResult RedisProtocolParser::OnPacket(PacketType pktType,
                                          MessageType msgType,
                                          const PacketEventHeader* header,
                                          const char* pkt,
                                          int32_t pktSize,
                                          int32_t pktRealSize,
                                          int32_t* offset) {
    RedisParser redis(pkt + *offset, pktSize - *offset);
    ParseResult result;
    LOG_TRACE(sLogger,
              ("message_type", MessageTypeToString(msgType))("redis date", charToHexString(pkt, pktSize, pktSize)));
    try {
        result = redis.parse();
    } catch (const std::runtime_error& re) {
        LOG_DEBUG(sLogger,
                  ("redis_parse_fail", re.what())("data", charToHexString(pkt, pktSize, pktSize))(
                      "srcPort", header->SrcPort)("dstPort", header->DstPort));
        return ParseResult_Fail;
    } catch (const std::exception& ex) {
        LOG_DEBUG(sLogger,
                  ("redis_parse_fail", ex.what())("data", charToHexString(pkt, pktSize, pktSize))(
                      "srcPort", header->SrcPort)("dstPort", header->DstPort));
        return ParseResult_Fail;
    } catch (...) {
        LOG_DEBUG(
            sLogger,
            ("redis_parse_fail", "Unknown failure occurred when parse")("data", charToHexString(pkt, pktSize, pktSize))(
                "srcPort", header->SrcPort)("dstPort", header->DstPort));
        return ParseResult_Fail;
    }

    if (result != ParseResult_OK) {
        return result;
    }
    bool insertSuccess = true;
    if (msgType == MessageType_Request) {
        insertSuccess = mCache.InsertReq([&](RedisRequestInfo* info) {
            info->TimeNano = header->TimeNano;
            info->ReqBytes = pktSize - *offset;
            info->Data = redis.redisData.GetCommands();
            LOG_TRACE(sLogger, ("redis insert req", info->ToString()));
        });
    } else if (msgType == MessageType_Response) {
        insertSuccess = mCache.InsertResp([&](RedisResponseInfo* info) {
            info->TimeNano = header->TimeNano;
            info->RespBytes = pktSize - *offset;
            info->IsOK = !redis.redisData.isError;
            info->Data = redis.redisData.GetCommands();
            LOG_TRACE(sLogger, ("redis insert resp", info->ToString()));
        });
    }
    return insertSuccess ? ParseResult_OK : ParseResult_Drop;
}

size_t RedisProtocolParser::FindBoundary(const SlsStringPiece& piece) {
    for (size_t i = 0; i < piece.mLen; ++i) {
        auto c = piece[i];
        if (c == kSimpleStringFlag || c == kErrorFlag || c == kArrayFlag || c == kBulkStringFlag || c == kNumberFlag) {
            return i;
        }
    }
    return std::string::npos;
}

bool RedisProtocolParser::GarbageCollection(size_t size_limit_bytes, uint64_t expireTimeNs) {
    return mCache.GarbageCollection(expireTimeNs);
}

int32_t RedisProtocolParser::GetCacheSize() {
    return this->mCache.GetRequestsSize() + this->mCache.GetResponsesSize();
}

std::ostream& operator<<(std::ostream& os, const RedisRequestInfo& info) {
    os << "TimeNano: " << info.TimeNano << " CMD: " << info.Data << " ReqBytes: " << info.ReqBytes;
    return os;
}

std::ostream& operator<<(std::ostream& os, const RedisResponseInfo& info) {
    os << "TimeNano: " << info.TimeNano << " isOK: " << info.IsOK << " RespBytes: " << info.RespBytes
       << " Data: " << info.Data;
    return os;
}
} // end of namespace logtail